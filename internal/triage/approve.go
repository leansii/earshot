// SPDX-License-Identifier: Apache-2.0

package triage

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leansii/earshot/internal/connector"
	"github.com/leansii/earshot/internal/core"
	"github.com/leansii/earshot/internal/scrub"
	"github.com/leansii/earshot/internal/store"
)

// TaskApprover is the human-gated bridge to the tracker. Approving a task is the
// only action that pushes a task into the tracker, per the project's configured
// TaskStrategy. The feedback-issue itself is auto-filed by the pipeline; tasks
// are gated here.
type TaskApprover struct {
	store   store.Store
	tracker connector.Tracker
	log     *slog.Logger
}

// NewTaskApprover builds a TaskApprover.
func NewTaskApprover(st store.Store, tracker connector.Tracker, log *slog.Logger) *TaskApprover {
	if log == nil {
		log = slog.Default()
	}
	return &TaskApprover{store: st, tracker: tracker, log: log}
}

// Approve marks a task approved and pushes it to the tracker using the project's
// strategy. For tracker "none" it records approval without any external write.
func (a *TaskApprover) Approve(ctx context.Context, taskID string) (core.Task, error) {
	task, err := a.store.Task(ctx, taskID)
	if err != nil {
		return core.Task{}, fmt.Errorf("approve: load task %s: %w", taskID, err)
	}
	if task.Status == core.StatusFiled {
		return core.Task{}, fmt.Errorf("approve: task %s already pushed (%s)", taskID, task.TrackerRef)
	}
	if task.Status == core.StatusRejected {
		return core.Task{}, fmt.Errorf("approve: task %s was rejected", taskID)
	}

	item, err := a.store.Item(ctx, task.ItemID)
	if err != nil {
		return core.Task{}, fmt.Errorf("approve: load feedback-issue: %w", err)
	}
	project, err := a.store.Project(ctx, task.ProjectID)
	if err != nil {
		return core.Task{}, fmt.Errorf("approve: load project: %w", err)
	}

	// Record the human decision before any external write.
	if err := a.store.UpdateTaskStatus(ctx, taskID, core.StatusApproved); err != nil {
		return core.Task{}, fmt.Errorf("approve: mark approved: %w", err)
	}

	// No tracker configured: approval is terminal, nothing is pushed.
	if project.Tracker == core.TrackerNone {
		a.log.Info("task approved (tracker=none, not pushed)", "task", taskID)
		return a.store.Task(ctx, taskID)
	}
	if item.GitHubIssueNumber == 0 {
		return core.Task{}, fmt.Errorf("approve: parent feedback-issue %s is not filed; cannot attach task", item.ID)
	}

	parent := connector.IssueRef{
		Owner: project.GitHubOwner, Repo: project.GitHubRepo,
		Number: item.GitHubIssueNumber, URL: item.GitHubIssueURL,
	}

	var ref, url string
	var number int
	switch strategy(project) {
	case core.StrategySubIssues:
		sub, err := a.tracker.CreateSubIssue(ctx, project.GitHubToken, parent, connector.IssueRequest{
			Owner: project.GitHubOwner, Repo: project.GitHubRepo,
			Title: task.Title, Body: taskBody(task), Labels: project.Labels,
		})
		if err != nil {
			return core.Task{}, fmt.Errorf("approve: create sub-issue: %w", err)
		}
		number, url, ref = sub.Number, sub.URL, fmt.Sprintf("#%d", sub.Number)

	case core.StrategyComments:
		c, err := a.tracker.AddComment(ctx, project.GitHubToken, parent, taskBody(task))
		if err != nil {
			return core.Task{}, fmt.Errorf("approve: add comment: %w", err)
		}
		number, url, ref = parent.Number, c.URL, "comment"

	case core.StrategyChecklist:
		body, err := a.checklistBody(ctx, item, task)
		if err != nil {
			return core.Task{}, err
		}
		if err := a.tracker.UpdateIssueBody(ctx, project.GitHubToken, parent, body); err != nil {
			return core.Task{}, fmt.Errorf("approve: update checklist: %w", err)
		}
		number, url, ref = parent.Number, parent.URL, "checklist"

	default:
		return core.Task{}, fmt.Errorf("approve: unknown task strategy %q", project.TaskStrategy)
	}

	if err := a.store.MarkTaskPushed(ctx, taskID, ref, number, url); err != nil {
		return core.Task{}, fmt.Errorf("approve: record pushed task: %w", err)
	}
	a.log.Info("task pushed", "task", taskID, "strategy", project.TaskStrategy, "ref", ref)
	return a.store.Task(ctx, taskID)
}

// Reject marks a task rejected. No tracker call is made.
func (a *TaskApprover) Reject(ctx context.Context, taskID string) error {
	task, err := a.store.Task(ctx, taskID)
	if err != nil {
		return fmt.Errorf("reject: load task %s: %w", taskID, err)
	}
	if task.Status == core.StatusFiled {
		return fmt.Errorf("reject: task %s already pushed", taskID)
	}
	return a.store.UpdateTaskStatus(ctx, taskID, core.StatusRejected)
}

func strategy(p core.Project) core.TaskStrategy {
	if p.TaskStrategy.Valid() {
		return p.TaskStrategy
	}
	return core.StrategySubIssues
}

// checklistBody rebuilds the feedback-issue body with a task-list of every
// approved/pushed task (including the one being approved now).
func (a *TaskApprover) checklistBody(ctx context.Context, item core.Item, current core.Task) (string, error) {
	tasks, err := a.store.TasksForItem(ctx, item.ID)
	if err != nil {
		return "", fmt.Errorf("approve: load tasks: %w", err)
	}
	var b strings.Builder
	b.WriteString(feedbackIssueBody(item))
	b.WriteString("\n### Tasks\n\n")
	for _, t := range tasks {
		// The current task is approved in this call; include approved+pushed.
		checked := t.Status == core.StatusFiled || t.ID == current.ID
		if t.Status == core.StatusRejected {
			continue
		}
		mark := " "
		if checked {
			mark = "x"
		}
		title, _ := scrub.Text(t.Title)
		fmt.Fprintf(&b, "- [%s] %s\n", mark, title)
	}
	return b.String(), nil
}

// taskBody renders a task into issue/comment markdown, scrubbed defensively.
func taskBody(task core.Task) string {
	desc, _ := scrub.Text(task.Description)
	var b strings.Builder
	if desc != "" {
		b.WriteString(desc)
		b.WriteString("\n\n")
	}
	b.WriteString("_Task triaged via Earshot._\n")
	return b.String()
}
