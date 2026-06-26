// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leansii/earshot/internal/apikey"
	"github.com/leansii/earshot/internal/core"
	"github.com/leansii/earshot/internal/memory"
	"github.com/leansii/earshot/internal/queue"
)

const testKey = "esk_testkey_abcdef"

func seed(t *testing.T, rpm int) (*memory.Backend, string) {
	t.Helper()
	b := memory.New()
	ctx := context.Background()
	proj, err := b.CreateProject(ctx, core.Project{Slug: "demo", Name: "Demo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateAppKey(ctx, core.AppKey{
		ProjectID: proj.ID, KeyHash: apikey.Hash(testKey), Prefix: "esk_test", RateLimitPerMin: rpm,
	}); err != nil {
		t.Fatal(err)
	}
	return b, proj.ID
}

func post(t *testing.T, h http.Handler, key string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	if s, ok := payload.(string); ok {
		body.WriteString(s)
	} else if err := json.NewEncoder(&body).Encode(payload); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/feedback", &body)
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestFeedback_RequiresAuth(t *testing.T) {
	b, _ := seed(t, 100)
	h := NewService(b, b, nil, nil).Routes()
	rr := post(t, h, "", map[string]string{"app_id": "a", "body": "hi"})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no key: got %d, want 401", rr.Code)
	}
	rr = post(t, h, "esk_wrong", map[string]string{"app_id": "a", "body": "hi"})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad key: got %d, want 401", rr.Code)
	}
}

func TestFeedback_Validation(t *testing.T) {
	b, _ := seed(t, 100)
	h := NewService(b, b, nil, nil).Routes()

	if rr := post(t, h, testKey, map[string]string{"body": "no app id"}); rr.Code != http.StatusBadRequest {
		t.Fatalf("missing app_id: got %d, want 400", rr.Code)
	}
	if rr := post(t, h, testKey, map[string]string{"app_id": "a"}); rr.Code != http.StatusBadRequest {
		t.Fatalf("missing body: got %d, want 400", rr.Code)
	}
	if rr := post(t, h, testKey, map[string]string{"app_id": "a", "body": "x", "severity": "nope"}); rr.Code != http.StatusBadRequest {
		t.Fatalf("bad severity: got %d, want 400", rr.Code)
	}
	if rr := post(t, h, testKey, "{not json"); rr.Code != http.StatusBadRequest {
		t.Fatalf("bad json: got %d, want 400", rr.Code)
	}
}

func TestFeedback_PersistsAndEnqueuesTransactionally(t *testing.T) {
	b, _ := seed(t, 100)
	h := NewService(b, b, nil, nil).Routes()

	rr := post(t, h, testKey, map[string]any{
		"app_id": "web", "version": "1.2.3", "route": "/settings", "severity": "high",
		"body": "the save button crashes",
	})
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	var resp feedbackResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID == "" || resp.Status != "accepted" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	jobs := b.TakeJobs()
	if len(jobs) != 1 {
		t.Fatalf("expected exactly 1 enqueued job, got %d", len(jobs))
	}
	if jobs[0].Kind != queue.KindEnrich || jobs[0].FeedbackID != resp.ID {
		t.Fatalf("job mismatch: %+v (feedback %s)", jobs[0], resp.ID)
	}
	if _, err := b.Feedback(context.Background(), resp.ID); err != nil {
		t.Fatalf("feedback should be persisted: %v", err)
	}
}

// failQueue fails every enqueue, to prove the transaction rolls back: no job is
// delivered and no feedback is committed.
type failQueue struct{}

func (failQueue) Enqueue(context.Context, core.Tx, queue.Job) error {
	return errors.New("boom")
}

func TestFeedback_RollsBackOnEnqueueFailure(t *testing.T) {
	b, _ := seed(t, 100)
	h := NewService(b, failQueue{}, nil, nil).Routes()

	rr := post(t, h, testKey, map[string]any{"app_id": "web", "body": "crash"})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", rr.Code)
	}
	if jobs := b.TakeJobs(); len(jobs) != 0 {
		t.Fatalf("no job must leak on rollback, got %d", len(jobs))
	}
}

func TestFeedback_RateLimited(t *testing.T) {
	b, _ := seed(t, 1) // 1 request/min
	h := NewService(b, b, nil, nil).Routes()
	payload := map[string]any{"app_id": "web", "body": "hello there"}

	if rr := post(t, h, testKey, payload); rr.Code != http.StatusAccepted {
		t.Fatalf("first request: got %d, want 202", rr.Code)
	}
	if rr := post(t, h, testKey, payload); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: got %d, want 429", rr.Code)
	}
}

func TestHealthz(t *testing.T) {
	b, _ := seed(t, 100)
	h := NewService(b, b, nil, nil).Routes()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("healthz: got %d, want 200", rr.Code)
	}
}
