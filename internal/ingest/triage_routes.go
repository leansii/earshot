// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/leansii/earshot/internal/core"
)

// itemView is the API representation of a triage item. It deliberately omits
// raw feedback; only the scrubbed body is ever exposed.
type itemView struct {
	ID                string `json:"id"`
	Category          string `json:"category"`
	Title             string `json:"title"`
	Summary           string `json:"summary"`
	ScrubbedBody      string `json:"scrubbed_body"`
	Status            string `json:"status"`
	FeedbackCount     int    `json:"feedback_count"`
	GitHubIssueNumber int    `json:"github_issue_number,omitempty"`
	GitHubIssueURL    string `json:"github_issue_url,omitempty"`
}

func toView(it core.Item) itemView {
	return itemView{
		ID: it.ID, Category: it.Category, Title: it.Title, Summary: it.Summary,
		ScrubbedBody: it.ScrubbedBody, Status: string(it.Status), FeedbackCount: it.FeedbackCount,
		GitHubIssueNumber: it.GitHubIssueNumber, GitHubIssueURL: it.GitHubIssueURL,
	}
}

func (s *Service) handleListTriage(w http.ResponseWriter, r *http.Request) {
	project, _ := r.Context().Value(ctxProject).(core.Project)
	status := core.Status(r.URL.Query().Get("status"))
	if status == "" {
		status = core.StatusNeedsTriage
	}
	items, err := s.store.ListItems(r.Context(), project.ID, status)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	views := make([]itemView, 0, len(items))
	for _, it := range items {
		views = append(views, toView(it))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": views})
}

// itemInProject loads an item and enforces tenant isolation: an item from
// another project is reported as not found.
func (s *Service) itemInProject(r *http.Request) (core.Item, bool) {
	project, _ := r.Context().Value(ctxProject).(core.Project)
	id := chi.URLParam(r, "id")
	it, err := s.store.Item(r.Context(), id)
	if err != nil || it.ProjectID != project.ID {
		return core.Item{}, false
	}
	return it, true
}

func (s *Service) handleApprove(w http.ResponseWriter, r *http.Request) {
	if s.approver == nil {
		writeErr(w, http.StatusNotImplemented, "triage not enabled")
		return
	}
	it, ok := s.itemInProject(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "item not found")
		return
	}
	filed, err := s.approver.Approve(r.Context(), it.ID)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toView(filed))
}

func (s *Service) handleReject(w http.ResponseWriter, r *http.Request) {
	if s.approver == nil {
		writeErr(w, http.StatusNotImplemented, "triage not enabled")
		return
	}
	it, ok := s.itemInProject(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "item not found")
		return
	}
	if err := s.approver.Reject(r.Context(), it.ID); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": it.ID, "status": string(core.StatusRejected)})
}
