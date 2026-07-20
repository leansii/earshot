// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestOpenAI returns an OpenAICompat pointed at a stub server that answers
// every completion with the given assistant text.
func newTestOpenAI(t *testing.T, provider, reply string) (*OpenAICompat, *[]openAIReq) {
	t.Helper()
	var seen []openAIReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("authorization"); got != "Bearer test-key" {
			t.Errorf("authorization header = %q, want Bearer test-key", got)
		}
		var req openAIReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		seen = append(seen, req)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": reply}}},
		})
	}))
	t.Cleanup(srv.Close)

	c, err := NewOpenAICompat(provider, "test-model", "test-key")
	if err != nil {
		t.Fatal(err)
	}
	c.url = srv.URL
	return c, &seen
}

func TestOpenAICompatEnrich(t *testing.T) {
	c, seen := newTestOpenAI(t, "deepseek",
		`{"category": "bug", "title": "Export crashes", "summary": "Crash on empty list"}`)

	got, err := c.Enrich(context.Background(), EnrichInput{Body: "export crashes", Route: "/export"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Category != "bug" || got.Title != "Export crashes" {
		t.Errorf("unexpected result: %+v", got)
	}

	req := (*seen)[0]
	if req.Model != "test-model" {
		t.Errorf("model = %q, want test-model", req.Model)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" {
		t.Errorf("expected [system, user] messages, got %+v", req.Messages)
	}
}

// Smaller models sometimes wrap JSON in a markdown fence despite instructions;
// the client must tolerate that.
func TestOpenAICompatEnrichStripsCodeFence(t *testing.T) {
	c, _ := newTestOpenAI(t, "gemini",
		"```json\n{\"category\": \"bug\", \"title\": \"T\", \"summary\": \"S\"}\n```")

	got, err := c.Enrich(context.Background(), EnrichInput{Body: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Category != "bug" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestOpenAICompatDecomposeTasks(t *testing.T) {
	c, _ := newTestOpenAI(t, "deepseek",
		`[{"title": "Fix export crash", "description": "Guard the empty-list case"}]`)

	tasks, err := c.DecomposeTasks(context.Background(), PlanInput{Category: "bug", Title: "T"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Title != "Fix export crash" {
		t.Errorf("unexpected tasks: %+v", tasks)
	}
}

func TestOpenAICompatUnknownProvider(t *testing.T) {
	if _, err := NewOpenAICompat("nope", "m", "k"); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestOpenAICompatModel(t *testing.T) {
	c, _ := newTestOpenAI(t, "deepseek", "{}")
	if got := c.Model(); got != "deepseek:test-model" {
		t.Errorf("Model() = %q", got)
	}
}
