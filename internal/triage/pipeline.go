// SPDX-License-Identifier: Apache-2.0

// Package triage runs the async enrichment pipeline and the human-gated
// approval that is the ONLY path to GitHub.
//
// Pipeline stages: enrich (LLM classify) -> dedup (embedding cosine) -> scrub
// (PII/secret redaction) -> triage (write a draft in needs_triage). The
// pipeline never creates a GitHub issue; that requires an explicit human
// approval (see Approver).
package triage

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/leansii/earshot/internal/core"
	"github.com/leansii/earshot/internal/dedup"
	"github.com/leansii/earshot/internal/embed"
	"github.com/leansii/earshot/internal/llm"
	"github.com/leansii/earshot/internal/queue"
	"github.com/leansii/earshot/internal/scrub"
	"github.com/leansii/earshot/internal/store"
)

// Pipeline enriches feedback into draft triage items.
type Pipeline struct {
	store     store.Store
	llm       llm.Classifier
	embedder  embed.Embedder
	threshold float32
	log       *slog.Logger
}

// NewPipeline builds a Pipeline. A zero threshold uses dedup.DefaultThreshold.
func NewPipeline(st store.Store, classifier llm.Classifier, embedder embed.Embedder, threshold float32, log *slog.Logger) *Pipeline {
	if threshold == 0 {
		threshold = dedup.DefaultThreshold
	}
	if log == nil {
		log = slog.Default()
	}
	return &Pipeline{store: st, llm: classifier, embedder: embedder, threshold: threshold, log: log}
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

// enrich is idempotent at the job level: it reads the feedback row, classifies,
// dedups, scrubs, and writes a draft. Re-running it for an already-linked
// feedback would create a duplicate item, so a production store would guard on
// the feedback->item mapping; that hardening is tracked as a follow-up.
func (p *Pipeline) enrich(ctx context.Context, feedbackID string) error {
	fb, err := p.store.Feedback(ctx, feedbackID)
	if err != nil {
		return fmt.Errorf("triage: load feedback %s: %w", feedbackID, err)
	}

	// 1. Enrich. The body is untrusted data; the classifier must not obey it.
	res, err := p.llm.Enrich(ctx, llm.EnrichInput{
		Body:     fb.Body,
		Route:    fb.Route,
		Severity: fb.Severity,
		Context:  fb.Context,
	})
	if err != nil {
		return fmt.Errorf("triage: enrich %s: %w", feedbackID, err)
	}

	// 2. Dedup against existing items in the same project.
	vec, err := p.embedder.Embed(ctx, fb.Body)
	if err != nil {
		return fmt.Errorf("triage: embed %s: %w", feedbackID, err)
	}
	candidates, err := p.store.ItemsForDedup(ctx, fb.ProjectID)
	if err != nil {
		return fmt.Errorf("triage: load dedup candidates: %w", err)
	}
	if match := dedup.Nearest(vec, candidates, p.threshold); match.IsDup {
		p.log.Info("near-duplicate linked",
			"feedback", feedbackID, "item", match.Item.ID, "score", match.Score)
		return p.store.LinkFeedback(ctx, feedbackID, match.Item.ID)
	}

	// 3. Scrub everything that could reach a (possibly public) issue.
	scrubbedBody, report := scrub.Text(fb.Body)
	title, _ := scrub.Text(res.Title)
	summary, _ := scrub.Text(res.Summary)
	if report.Total() > 0 {
		p.log.Info("scrubbed secrets/PII from feedback",
			"feedback", feedbackID, "redactions", report.Total())
	}

	// 4. Triage: write a DRAFT in needs_triage. No GitHub issue is created here.
	item, err := p.store.CreateItem(ctx, core.Item{
		ProjectID:    fb.ProjectID,
		Category:     res.Category,
		Title:        title,
		Summary:      summary,
		ScrubbedBody: scrubbedBody,
		Status:       core.StatusNeedsTriage,
		Embedding:    vec,
	})
	if err != nil {
		return fmt.Errorf("triage: create item: %w", err)
	}
	if err := p.store.LinkFeedback(ctx, feedbackID, item.ID); err != nil {
		return fmt.Errorf("triage: link feedback: %w", err)
	}

	p.log.Info("draft created", "item", item.ID, "status", item.Status, "category", res.Category)
	return nil
}
