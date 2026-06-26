// SPDX-License-Identifier: Apache-2.0

// Package ingest is the fast, dumb HTTP front door. It authenticates a request,
// validates it, and ENQUEUES an enrich job in the SAME database transaction as
// the feedback insert (transactional outbox), then returns 202. No LLM or
// GitHub calls ever happen on the request path.
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/leansii/earshot/internal/apikey"
	"github.com/leansii/earshot/internal/core"
	"github.com/leansii/earshot/internal/queue"
	"github.com/leansii/earshot/internal/store"
	"github.com/leansii/earshot/internal/triage"
)

// maxBodyBytes caps request size to keep the front door cheap.
const maxBodyBytes = 64 * 1024

type ctxKey int

const (
	ctxProject ctxKey = iota
	ctxKeyRec
)

// Service handles ingest HTTP requests.
type Service struct {
	store    store.Store
	queue    queue.Queue
	approver *triage.Approver
	log      *slog.Logger
	lim      *limiter
}

// NewService builds an ingest Service. approver may be nil to disable the
// human-triage HTTP endpoints (e.g. an ingest-only deployment).
func NewService(st store.Store, q queue.Queue, approver *triage.Approver, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: st, queue: q, approver: approver, log: log, lim: newLimiter()}
}

// Routes returns the HTTP router for the ingest + triage API.
func (s *Service) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", s.handleHealth)
	r.Group(func(r chi.Router) {
		r.Use(s.authMiddleware)
		r.Post("/v1/feedback", s.handleFeedback)
		// Human triage gate. Approving here is the ONLY HTTP path that can
		// reach GitHub, and only via an explicit operator call.
		r.Get("/v1/triage", s.handleListTriage)
		r.Post("/v1/triage/{id}/approve", s.handleApprove)
		r.Post("/v1/triage/{id}/reject", s.handleReject)
	})
	return r
}

func (s *Service) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// authMiddleware authenticates by API key and enforces per-key rate limiting.
// It resolves the key to exactly one project and stashes both in the context;
// downstream handlers never see another tenant's data.
func (s *Service) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := extractKey(r)
		if raw == "" {
			writeErr(w, http.StatusUnauthorized, "missing API key")
			return
		}
		key, err := s.store.AppKeyByHash(r.Context(), apikey.Hash(raw))
		if err != nil {
			// Same response for unknown/revoked keys — don't leak which.
			writeErr(w, http.StatusUnauthorized, "invalid API key")
			return
		}
		if !s.lim.Allow(key.ID, key.RateLimitPerMin) {
			writeErr(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		project, err := s.store.Project(r.Context(), key.ProjectID)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "invalid API key")
			return
		}
		ctx := context.WithValue(r.Context(), ctxProject, project)
		ctx = context.WithValue(ctx, ctxKeyRec, key)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// feedbackRequest is the public ingest payload.
type feedbackRequest struct {
	AppID    string         `json:"app_id"`
	Version  string         `json:"version"`
	UserRef  string         `json:"user_ref"`
	Route    string         `json:"route"`
	Severity string         `json:"severity"`
	Body     string         `json:"body"`
	Context  map[string]any `json:"context"`
}

type feedbackResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (s *Service) handleFeedback(w http.ResponseWriter, r *http.Request) {
	project, _ := r.Context().Value(ctxProject).(core.Project)

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var req feedbackRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	if err := validate(req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	fb := core.Feedback{
		ProjectID: project.ID,
		AppID:     strings.TrimSpace(req.AppID),
		Version:   req.Version,
		UserRef:   req.UserRef,
		Route:     req.Route,
		Severity:  req.Severity,
		Body:      strings.TrimSpace(req.Body),
		Context:   req.Context,
	}

	// Transactional outbox: persist feedback and enqueue its enrich job in one
	// transaction. Either both happen or neither does.
	tx, err := s.store.BeginTx(r.Context())
	if err != nil {
		s.log.Error("begin tx", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(r.Context())
		}
	}()

	saved, err := s.store.CreateFeedback(r.Context(), tx, fb)
	if err != nil {
		s.log.Error("create feedback", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := s.queue.Enqueue(r.Context(), tx, queue.Job{Kind: queue.KindEnrich, FeedbackID: saved.ID}); err != nil {
		s.log.Error("enqueue", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.log.Error("commit", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	committed = true

	s.log.Info("feedback accepted", "project", project.ID, "feedback", saved.ID)
	writeJSON(w, http.StatusAccepted, feedbackResponse{ID: saved.ID, Status: "accepted"})
}

func validate(req feedbackRequest) error {
	if strings.TrimSpace(req.AppID) == "" {
		return errors.New("app_id is required")
	}
	if strings.TrimSpace(req.Body) == "" {
		return errors.New("body is required")
	}
	if len(req.Body) > maxBodyBytes {
		return errors.New("body too long")
	}
	if req.Severity != "" && !validSeverity(req.Severity) {
		return errors.New("severity must be one of: low, medium, high, critical")
	}
	return nil
}

func validSeverity(s string) bool {
	switch s {
	case "low", "medium", "high", "critical":
		return true
	default:
		return false
	}
}

func extractKey(r *http.Request) string {
	if k := strings.TrimSpace(r.Header.Get("X-API-Key")); k != "" {
		return k
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
