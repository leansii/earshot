// SPDX-License-Identifier: Apache-2.0

package triage_test

import (
	"context"
	"strings"
	"testing"

	"github.com/leansii/earshot/internal/connector"
	"github.com/leansii/earshot/internal/core"
	"github.com/leansii/earshot/internal/embed"
	"github.com/leansii/earshot/internal/llm"
	"github.com/leansii/earshot/internal/memory"
	"github.com/leansii/earshot/internal/queue"
	"github.com/leansii/earshot/internal/triage"
)

type harness struct {
	b        *memory.Backend
	project  core.Project
	pipeline *triage.Pipeline
	approver *triage.TaskApprover
	tracker  *connector.Mock
}

// newHarness builds a harness; mutate lets a test tweak the project before it is
// created (strategy, tracker, autofile, ...).
func newHarness(t *testing.T, mutate func(*core.Project)) *harness {
	t.Helper()
	ctx := context.Background()
	b := memory.New()
	p := core.Project{
		Slug: "demo", GitHubOwner: "leansii", GitHubRepo: "earshot",
		GitHubToken: "ghp_projecttoken", Labels: []string{"feedback", "from-earshot"},
		Tracker: core.TrackerGitHub, TaskStrategy: core.StrategySubIssues, FeedbackAutoFile: true,
	}
	if mutate != nil {
		mutate(&p)
	}
	project, err := b.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	tr := connector.NewMock()
	return &harness{
		b:        b,
		project:  project,
		pipeline: triage.NewPipeline(b, llm.NewFake(""), embed.NewFake("hash-64"), tr, 0, nil),
		approver: triage.NewTaskApprover(b, tr, nil),
		tracker:  tr,
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

func (h *harness) items(t *testing.T) []core.Item {
	t.Helper()
	items, err := h.b.ListItems(context.Background(), h.project.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func (h *harness) tasks(t *testing.T, itemID string) []core.Task {
	t.Helper()
	tasks, err := h.b.TasksForItem(context.Background(), itemID)
	if err != nil {
		t.Fatal(err)
	}
	return tasks
}

// TestPipeline_AutoFilesIssueAndDraftsTasks is the central guarantee of the new
// model: the feedback-issue auto-files, the agent drafts tasks, and NO task is
// pushed to the tracker by the pipeline.
func TestPipeline_AutoFilesIssueAndDraftsTasks(t *testing.T) {
	h := newHarness(t, nil)
	fb := h.addFeedback(t, "the save button crashes when I click it")
	h.run(t, fb.ID)

	items := h.items(t)
	if len(items) != 1 {
		t.Fatalf("expected 1 feedback-issue, got %d", len(items))
	}
	if items[0].Status != core.StatusFiled || items[0].GitHubIssueNumber == 0 {
		t.Fatalf("feedback-issue should be auto-filed, got %+v", items[0])
	}
	if h.tracker.FeedbackIssueCount() != 1 {
		t.Fatalf("expected 1 filed feedback-issue, got %d", h.tracker.FeedbackIssueCount())
	}

	tasks := h.tasks(t, items[0].ID)
	if len(tasks) != 3 { // bug → reproduce/fix/test
		t.Fatalf("expected 3 draft tasks for a bug, got %d", len(tasks))
	}
	for _, tk := range tasks {
		if tk.Status != core.StatusNeedsTriage {
			t.Fatalf("task %s should be needs_triage, got %s", tk.ID, tk.Status)
		}
	}
	if h.tracker.TaskPushCount() != 0 {
		t.Fatalf("pipeline must NOT push tasks; got %d pushes", h.tracker.TaskPushCount())
	}
}

func TestPipeline_NoAutoFileWhenDisabled(t *testing.T) {
	h := newHarness(t, func(p *core.Project) { p.FeedbackAutoFile = false })
	fb := h.addFeedback(t, "the save button crashes")
	h.run(t, fb.ID)

	items := h.items(t)
	if items[0].Status != core.StatusNeedsTriage || items[0].GitHubIssueNumber != 0 {
		t.Fatalf("issue must NOT be filed when autofile off: %+v", items[0])
	}
	if h.tracker.FeedbackIssueCount() != 0 {
		t.Fatal("tracker should not be called when autofile is off")
	}
	if len(h.tasks(t, items[0].ID)) == 0 {
		t.Fatal("tasks should still be drafted")
	}
}

func TestPipeline_TrackerNoneKeepsEverythingInDB(t *testing.T) {
	h := newHarness(t, func(p *core.Project) { p.Tracker = core.TrackerNone })
	fb := h.addFeedback(t, "please add dark mode")
	h.run(t, fb.ID)

	if h.tracker.FeedbackIssueCount() != 0 {
		t.Fatal("tracker=none must not file anything")
	}
	if len(h.tasks(t, h.items(t)[0].ID)) == 0 {
		t.Fatal("tasks should still be drafted in DB")
	}
}

func TestPipeline_LinksNearDuplicates(t *testing.T) {
	h := newHarness(t, nil)
	a := h.addFeedback(t, "the save button crashes when I click it")
	h.run(t, a.ID)
	b := h.addFeedback(t, "the save button crashes when I click it")
	h.run(t, b.ID)

	items := h.items(t)
	if len(items) != 1 {
		t.Fatalf("near-duplicate should link; got %d issues", len(items))
	}
	if items[0].FeedbackCount != 2 {
		t.Fatalf("feedback_count = %d, want 2", items[0].FeedbackCount)
	}
	if h.tracker.FeedbackIssueCount() != 1 {
		t.Fatalf("only the first should file an issue; got %d", h.tracker.FeedbackIssueCount())
	}
}

func TestPipeline_ScrubsBeforeFiling(t *testing.T) {
	h := newHarness(t, nil)
	fb := h.addFeedback(t, "the export throws an error; token ghp_abcdefghijklmnopqrstuvwxyz0123456789AB")
	h.run(t, fb.ID)

	if h.tracker.FeedbackIssueCount() != 1 {
		t.Fatal("expected the issue to be filed")
	}
	body := h.tracker.Issues[0].Body
	if strings.Contains(body, "ghp_abcdefghijklmnopqrstuvwxyz0123456789AB") {
		t.Fatalf("secret leaked into filed issue: %q", body)
	}
	if !strings.Contains(body, "[REDACTED:github-pat]") {
		t.Fatalf("expected redaction in filed issue body, got %q", body)
	}
}

// TestPipeline_TitleNeverLeaksTruncatedSecret is a regression test: a secret
// near the start of a long body must not survive in the (truncated) title.
// Scrubbing before enrichment guarantees there is no secret left to truncate.
func TestPipeline_TitleNeverLeaksTruncatedSecret(t *testing.T) {
	h := newHarness(t, nil)
	fb := h.addFeedback(t, "ghp_abcdefghijklmnopqrstuvwxyz0123456789AB is my token and the save "+
		"button crashes badly when clicked repeatedly on the settings page over and over")
	h.run(t, fb.ID)

	it := h.items(t)[0]
	if strings.Contains(it.Title, "ghp_") {
		t.Fatalf("title leaked a (possibly truncated) token: %q", it.Title)
	}
	if strings.Contains(h.tracker.Issues[0].Title, "ghp_") {
		t.Fatalf("filed issue title leaked a token: %q", h.tracker.Issues[0].Title)
	}
}

// TestApproveTask_SubIssuePushesViaProjectToken is the human-gate guarantee at
// the task level: only an explicit approval pushes a task, as a sub-issue, using
// the project's own token.
func TestApproveTask_SubIssuePushesViaProjectToken(t *testing.T) {
	h := newHarness(t, nil)
	fb := h.addFeedback(t, "the save button crashes")
	h.run(t, fb.ID)
	task := h.tasks(t, h.items(t)[0].ID)[0]

	if h.tracker.TaskPushCount() != 0 {
		t.Fatal("no task should be pushed before approval")
	}

	pushed, err := h.approver.Approve(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if len(h.tracker.SubIssues) != 1 {
		t.Fatalf("expected 1 sub-issue, got %d", len(h.tracker.SubIssues))
	}
	if pushed.Status != core.StatusFiled || !strings.HasPrefix(pushed.TrackerRef, "#") {
		t.Fatalf("task not marked pushed: %+v", pushed)
	}
	// Tokens recorded: one for the feedback-issue, one for the sub-issue, both
	// the project's token.
	for _, tok := range h.tracker.Tokens {
		if tok != "ghp_projecttoken" {
			t.Fatalf("expected per-project token, got %q", tok)
		}
	}
	if _, err := h.approver.Approve(context.Background(), task.ID); err == nil {
		t.Fatal("re-approving a pushed task should error")
	}
}

func TestApproveTask_ChecklistStrategy(t *testing.T) {
	h := newHarness(t, func(p *core.Project) { p.TaskStrategy = core.StrategyChecklist })
	fb := h.addFeedback(t, "the save button crashes")
	h.run(t, fb.ID)
	task := h.tasks(t, h.items(t)[0].ID)[0]

	if _, err := h.approver.Approve(context.Background(), task.ID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if len(h.tracker.BodyUpdates) != 1 {
		t.Fatalf("checklist should update the issue body once, got %d", len(h.tracker.BodyUpdates))
	}
	if !strings.Contains(h.tracker.BodyUpdates[0], "- [x]") {
		t.Fatalf("checklist body should contain a checked item: %q", h.tracker.BodyUpdates[0])
	}
}

func TestApproveTask_CommentsStrategy(t *testing.T) {
	h := newHarness(t, func(p *core.Project) { p.TaskStrategy = core.StrategyComments })
	fb := h.addFeedback(t, "the save button crashes")
	h.run(t, fb.ID)
	task := h.tasks(t, h.items(t)[0].ID)[0]

	if _, err := h.approver.Approve(context.Background(), task.ID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if len(h.tracker.Comments) != 1 {
		t.Fatalf("comments strategy should post 1 comment, got %d", len(h.tracker.Comments))
	}
}

func TestApproveTask_TrackerNoneApprovesWithoutPush(t *testing.T) {
	h := newHarness(t, func(p *core.Project) { p.Tracker = core.TrackerNone })
	fb := h.addFeedback(t, "the save button crashes")
	h.run(t, fb.ID)
	task := h.tasks(t, h.items(t)[0].ID)[0]

	pushed, err := h.approver.Approve(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if pushed.Status != core.StatusApproved {
		t.Fatalf("tracker=none task should be approved (not filed), got %s", pushed.Status)
	}
	if h.tracker.TaskPushCount() != 0 {
		t.Fatal("tracker=none must not push")
	}
}

func TestRejectTask_NeverPushes(t *testing.T) {
	h := newHarness(t, nil)
	fb := h.addFeedback(t, "the page is slow")
	h.run(t, fb.ID)
	task := h.tasks(t, h.items(t)[0].ID)[0]

	if err := h.approver.Reject(context.Background(), task.ID); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if h.tracker.TaskPushCount() != 0 {
		t.Fatal("reject must not push to tracker")
	}
	got, _ := h.b.Task(context.Background(), task.ID)
	if got.Status != core.StatusRejected {
		t.Fatalf("status = %q, want rejected", got.Status)
	}
}
