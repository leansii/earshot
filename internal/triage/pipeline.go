// SPDX-License-Identifier: Apache-2.0

// Package triage runs the async pipeline and the human-gated task approval.
//
// Pipeline: enrich (classify) -> dedup (cosine) -> scrub (PII/secrets) ->
// auto-file the feedback-issue to the tracker (if the project opts in) ->
// triage-agent decomposes it into Tasks (status needs_triage).
//
// The human gate now lives at the TASK level: tasks are stored in Earshot's DB
// (the source of truth) and are pushed to the tracker only on explicit
// approval. Scrubbing is a hard barrier — a feedback-issue is never filed with
// un-scrubbed content.
package triage

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leansii/earshot/internal/connector"
	"github.com/leansii/earshot/internal/core"
	"github.com/leansii/earshot/internal/dedup"
	"github.com/leansii/earshot/internal/embed"
	"github.com/leansii/earshot/internal/llm"
	"github.com/leansii/earshot/internal/queue"
	"github.com/leansii/earshot/internal/scrub"
	"github.com/leansii/earshot/internal/store"
)

// Pipeline enriches feedback into a filed feedback-issue plus draft tasks.
type Pipeline struct {
	store     store.Store
	provider  llm.Provider
	embedder  embed.Embedder
	tracker   connector.Tracker
	threshold float32
	log       *slog.Logger
}

// NewPipeline builds a Pipeline. A zero threshold uses dedup.DefaultThreshold.
func NewPipeline(st store.Store, provider llm.Provider, embedder embed.Embedder, tracker connector.Tracker, threshold float32, log *slog.Logger) *Pipeline {
	if threshold == 0 {
		threshold = dedup.DefaultThreshold
	}
	if log == nil {
		log = slog.Default()
	}
	return &Pipeline{store: st, provider: provider, embedder: embedder, tracker: tracker, threshold: threshold, log: log}
}

// Handle dispatches a queue job. It satisfies queue.Handler.
func (p *Pipeline) Handle(ctx context.Context, job queue.Job) error {
	switch job.Kind {
	case queue.KindEnrich:
		return p.enrich(ctx, job.FeedbackID)
	default:
		return fmt.Errorf("triage: unknown job kind %q", job.Kind)
	}
}

func (p *Pipeline) enrich(ctx context.Context, feedbackID string) error {
	fb, err := p.store.Feedback(ctx, feedbackID)
	if err != nil {
		return fmt.Errorf("triage: load feedback %s: %w", feedbackID, err)
	}
	project, err := p.store.Project(ctx, fb.ProjectID)
	if err != nil {
		return fmt.Errorf("triage: load project: %w", err)
	}

	// 1. Enrich. Body is untrusted data; the model must not obey it.
	res, err := p.provider.Enrich(ctx, llm.EnrichInput{
		Body: fb.Body, Route: fb.Route, Severity: fb.Severity, Context: fb.Context,
	})
	if err != nil {
		return fmt.Errorf("triage: enrich %s: %w", feedbackID, err)
	}

	// 2. Dedup against existing feedback-issues in the project.
	vec, err := p.embedder.Embed(ctx, fb.Body)
	if err != nil {
		return fmt.Errorf("triage: embed %s: %w", feedbackID, err)
	}
	candidates, err := p.store.ItemsForDedup(ctx, fb.ProjectID)
	if err != nil {
		return fmt.Errorf("triage: load dedup candidates: %w", err)
	}
	if match := dedup.Nearest(vec, candidates, p.threshold); match.IsDup {
		p.log.Info("near-duplicate linked", "feedback", feedbackID, "item", match.Item.ID, "score", match.Score)
		return p.store.LinkFeedback(ctx, feedbackID, match.Item.ID)
	}

	// 3. Scrub everything that could reach a (possibly public) issue.
	scrubbedBody, report := scrub.Text(fb.Body)
	title, _ := scrub.Text(res.Title)
	summary, _ := scrub.Text(res.Summary)
	if report.Total() > 0 {
		p.log.Info("scrubbed secrets/PII", "feedback", feedbackID, "redactions", report.Total())
	}

	// 4. Create the feedback-issue (source of truth in our DB).
	item, err := p.store.CreateItem(ctx, core.Item{
		ProjectID: fb.ProjectID, Category: res.Category, Title: title, Summary: summary,
		ScrubbedBody: scrubbedBody, Status: core.StatusNeedsTriage, Embedding: vec,
	})
	if err != nil {
		return fmt.Errorf("triage: create feedback-issue: %w", err)
	}
	if err := p.store.LinkFeedback(ctx, feedbackID, item.ID); err != nil {
		return fmt.Errorf("triage: link feedback: %w", err)
	}

	// 5. Auto-file the feedback-issue to the tracker, if the project opts in.
	//    Only scrubbed content leaves the system.
	if project.FeedbackAutoFile && project.Tracker != core.TrackerNone {
		ref, err := p.tracker.CreateIssue(ctx, project.GitHubToken, connector.IssueRequest{
			Owner: project.GitHubOwner, Repo: project.GitHubRepo,
			Title: item.Title, Body: feedbackIssueBody(item), Labels: project.Labels,
		})
		if err != nil {
			return fmt.Errorf("triage: file feedback-issue: %w", err)
		}
		if err := p.store.MarkItemFiled(ctx, item.ID, ref.Number, ref.URL); err != nil {
			return fmt.Errorf("triage: record filed feedback-issue: %w", err)
		}
		item.GitHubIssueNumber, item.GitHubIssueURL = ref.Number, ref.URL
		p.log.Info("feedback-issue filed", "item", item.ID, "issue", ref.Number)
	}

	// 6. Triage-agent: decompose into tasks. Tasks are drafts (needs_triage)
	//    and are NOT pushed anywhere — a human approves each one.
	tasks, err := p.provider.DecomposeTasks(ctx, llm.PlanInput{
		Category: res.Category, Title: item.Title, Summary: item.Summary, ScrubbedBody: item.ScrubbedBody,
	})
	if err != nil {
		return fmt.Errorf("triage: decompose tasks: %w", err)
	}
	for _, t := range tasks {
		if strings.TrimSpace(t.Title) == "" {
			continue
		}
		if _, err := p.store.CreateTask(ctx, core.Task{
			ProjectID: fb.ProjectID, ItemID: item.ID,
			Title: t.Title, Description: t.Description, Status: core.StatusNeedsTriage,
		}); err != nil {
			return fmt.Errorf("triage: create task: %w", err)
		}
	}
	p.log.Info("draft tasks created", "item", item.ID, "count", len(tasks))
	return nil
}

// feedbackIssueBody renders the feedback-issue markdown from scrubbed content.
func feedbackIssueBody(item core.Item) string {
	body, _ := scrub.Text(item.ScrubbedBody)
	summary, _ := scrub.Text(item.Summary)
	var b strings.Builder
	fmt.Fprintf(&b, "**Category:** %s\n\n", item.Category)
	if summary != "" {
		fmt.Fprintf(&b, "**Summary:** %s\n\n", summary)
	}
	b.WriteString("**Feedback (scrubbed):**\n\n> ")
	b.WriteString(strings.ReplaceAll(body, "\n", "\n> "))
	b.WriteString("\n\n---\n_Filed by Earshot. Tasks are added after human triage._\n")
	return b.String()
}
