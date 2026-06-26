// SPDX-License-Identifier: Apache-2.0

package triage_test

import (
	"context"
	"strings"
	"testing"

	"github.com/leansii/earshot/internal/core"
	"github.com/leansii/earshot/internal/embed"
	"github.com/leansii/earshot/internal/ghissue"
	"github.com/leansii/earshot/internal/llm"
	"github.com/leansii/earshot/internal/memory"
	"github.com/leansii/earshot/internal/queue"
	"github.com/leansii/earshot/internal/triage"
)

type harness struct {
	b        *memory.Backend
	project  core.Project
	pipeline *triage.Pipeline
	approver *triage.Approver
	gh       *ghissue.Mock
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	b := memory.New()
	project, err := b.CreateProject(ctx, core.Project{
		Slug: "demo", GitHubOwner: "leansii", GitHubRepo: "earshot",
		GitHubToken: "ghp_projecttoken", Labels: []string{"feedback", "from-earshot"},
	})
	if err != nil {
		t.Fatal(err)
	}
	gh := ghissue.NewMock()
	return &harness{
		b:        b,
		project:  project,
		pipeline: triage.NewPipeline(b, llm.NewFake(""), embed.NewFake("hash-64"), 0, nil),
		approver: triage.NewApprover(b, gh, nil),
		gh:       gh,
	}
}

func (h *harness) addFeedback(t *testing.T, body string) core.Feedback {
	t.Helper()
	ctx := context.Background()
	tx, err := h.b.BeginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := h.b.CreateFeedback(ctx, tx, core.Feedback{ProjectID: h.project.ID, AppID: "web", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return fb
}

func (h *harness) run(t *testing.T, feedbackID string) {
	t.Helper()
	if err := h.pipeline.Handle(context.Background(), queue.Job{Kind: queue.KindEnrich, FeedbackID: feedbackID}); err != nil {
		t.Fatalf("pipeline: %v", err)
	}
}

func (h *harness) drafts(t *testing.T) []core.Item {
	t.Helper()
	items, err := h.b.ListItems(context.Background(), h.project.ID, core.StatusNeedsTriage)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// TestPipeline_CreatesDraftAndNeverAutoFiles is the central guarantee: the
// pipeline produces a needs_triage draft and NOTHING reaches GitHub.
func TestPipeline_CreatesDraftAndNeverAutoFiles(t *testing.T) {
	h := newHarness(t)
	fb := h.addFeedback(t, "the save button crashes when I click it")
	h.run(t, fb.ID)

	drafts := h.drafts(t)
	if len(drafts) != 1 {
		t.Fatalf("expected 1 draft, got %d", len(drafts))
	}
	d := drafts[0]
	if d.Status != core.StatusNeedsTriage {
		t.Fatalf("status = %q, want needs_triage", d.Status)
	}
	if d.Category != "bug" {
		t.Fatalf("category = %q, want bug", d.Category)
	}
	if d.FeedbackCount != 1 {
		t.Fatalf("feedback_count = %d, want 1", d.FeedbackCount)
	}
	if h.gh.CallCount() != 0 {
		t.Fatalf("GitHub was called %d times during enrichment — must be 0", h.gh.CallCount())
	}
}

func TestPipeline_LinksNearDuplicates(t *testing.T) {
	h := newHarness(t)
	a := h.addFeedback(t, "the save button crashes when I click it")
	h.run(t, a.ID)
	b := h.addFeedback(t, "the save button crashes when I click it") // identical
	h.run(t, b.ID)

	drafts := h.drafts(t)
	if len(drafts) != 1 {
		t.Fatalf("near-duplicate should link, not create; got %d drafts", len(drafts))
	}
	if drafts[0].FeedbackCount != 2 {
		t.Fatalf("feedback_count = %d, want 2", drafts[0].FeedbackCount)
	}
}

func TestPipeline_ScrubsSecretsBeforeTriage(t *testing.T) {
	h := newHarness(t)
	fb := h.addFeedback(t, "email me at hacker@evil.com about this")
	h.run(t, fb.ID)

	d := h.drafts(t)[0]
	if strings.Contains(d.ScrubbedBody, "hacker@evil.com") {
		t.Fatalf("email leaked into draft body: %q", d.ScrubbedBody)
	}
	if !strings.Contains(d.ScrubbedBody, "[REDACTED:email]") {
		t.Fatalf("expected redaction placeholder, got %q", d.ScrubbedBody)
	}
	if strings.Contains(d.Title, "hacker@evil.com") {
		t.Fatalf("email leaked into title: %q", d.Title)
	}
}

// TestApprove_FilesScrubbedIssueViaPerProjectToken is the human-gate guarantee:
// only an explicit approval files an issue, it uses the project's own token,
// and the issue contains scrubbed content.
func TestApprove_FilesScrubbedIssueViaPerProjectToken(t *testing.T) {
	h := newHarness(t)
	fb := h.addFeedback(t, "the export throws an error; my token was ghp_abcdefghijklmnopqrstuvwxyz0123456789AB")
	h.run(t, fb.ID)
	item := h.drafts(t)[0]

	if h.gh.CallCount() != 0 {
		t.Fatal("no issue should exist before approval")
	}

	filed, err := h.approver.Approve(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}

	if h.gh.CallCount() != 1 {
		t.Fatalf("expected exactly 1 GitHub call, got %d", h.gh.CallCount())
	}
	req := h.gh.Calls[0]
	if req.Owner != "leansii" || req.Repo != "earshot" {
		t.Fatalf("issue targeted wrong repo: %+v", req)
	}
	if h.gh.Tokens[0] != "ghp_projecttoken" {
		t.Fatalf("expected per-project token, got %q", h.gh.Tokens[0])
	}
	if strings.Contains(req.Body, "ghp_abcdefghijklmnopqrstuvwxyz0123456789AB") {
		t.Fatalf("secret leaked into issue body: %q", req.Body)
	}
	if !strings.Contains(strings.Join(req.Labels, ","), "from-earshot") {
		t.Fatalf("project labels not applied: %v", req.Labels)
	}
	if filed.Status != core.StatusFiled || filed.GitHubIssueNumber != 1 {
		t.Fatalf("item not marked filed: %+v", filed)
	}

	// Re-approving must refuse.
	if _, err := h.approver.Approve(context.Background(), item.ID); err == nil {
		t.Fatal("re-approving a filed item should error")
	}
}

func TestReject_NeverCallsGitHub(t *testing.T) {
	h := newHarness(t)
	fb := h.addFeedback(t, "the page is slow to load")
	h.run(t, fb.ID)
	item := h.drafts(t)[0]

	if err := h.approver.Reject(context.Background(), item.ID); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if h.gh.CallCount() != 0 {
		t.Fatal("reject must not call GitHub")
	}
	got, _ := h.b.Item(context.Background(), item.ID)
	if got.Status != core.StatusRejected {
		t.Fatalf("status = %q, want rejected", got.Status)
	}
}
