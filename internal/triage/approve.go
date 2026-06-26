// SPDX-License-Identifier: Apache-2.0

package triage

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leansii/earshot/internal/core"
	"github.com/leansii/earshot/internal/ghissue"
	"github.com/leansii/earshot/internal/scrub"
	"github.com/leansii/earshot/internal/store"
)

// Approver is the human-gated bridge to GitHub. Approve is the only function in
// Earshot that causes an issue to be filed, and it is invoked exclusively from
// an explicit operator action (HTTP endpoint or CLI), never from the pipeline.
type Approver struct {
	store  store.Store
	issuer ghissue.Issuer
	log    *slog.Logger
}

// NewApprover builds an Approver.
func NewApprover(st store.Store, issuer ghissue.Issuer, log *slog.Logger) *Approver {
	if log == nil {
		log = slog.Default()
	}
	return &Approver{store: st, issuer: issuer, log: log}
}

// Approve files the item's draft as a GitHub issue using the project's
// per-project token, then records the issue. It refuses to re-file.
func (a *Approver) Approve(ctx context.Context, itemID string) (core.Item, error) {
	item, err := a.store.Item(ctx, itemID)
	if err != nil {
		return core.Item{}, fmt.Errorf("approve: load item %s: %w", itemID, err)
	}
	if item.Status == core.StatusFiled {
		return core.Item{}, fmt.Errorf("approve: item %s already filed as #%d", itemID, item.GitHubIssueNumber)
	}
	if item.Status == core.StatusRejected {
		return core.Item{}, fmt.Errorf("approve: item %s was rejected", itemID)
	}

	project, err := a.store.Project(ctx, item.ProjectID)
	if err != nil {
		return core.Item{}, fmt.Errorf("approve: load project: %w", err)
	}
	if project.GitHubOwner == "" || project.GitHubRepo == "" {
		return core.Item{}, fmt.Errorf("approve: project %s has no GitHub repo configured", project.ID)
	}

	// Record the human decision before the external write.
	if err := a.store.UpdateItemStatus(ctx, itemID, core.StatusApproved); err != nil {
		return core.Item{}, fmt.Errorf("approve: mark approved: %w", err)
	}

	res, err := a.issuer.CreateIssue(ctx, project.GitHubToken, ghissue.IssueRequest{
		Owner:  project.GitHubOwner,
		Repo:   project.GitHubRepo,
		Title:  item.Title,
		Body:   issueBody(item),
		Labels: project.Labels,
	})
	if err != nil {
		return core.Item{}, fmt.Errorf("approve: file issue: %w", err)
	}

	if err := a.store.MarkItemFiled(ctx, itemID, res.Number, res.URL); err != nil {
		return core.Item{}, fmt.Errorf("approve: record filed issue: %w", err)
	}

	a.log.Info("issue filed", "item", itemID, "issue", res.Number, "url", res.URL)
	return a.store.Item(ctx, itemID)
}

// Reject marks an item as rejected. No GitHub call is made.
func (a *Approver) Reject(ctx context.Context, itemID string) error {
	item, err := a.store.Item(ctx, itemID)
	if err != nil {
		return fmt.Errorf("reject: load item %s: %w", itemID, err)
	}
	if item.Status == core.StatusFiled {
		return fmt.Errorf("reject: item %s already filed", itemID)
	}
	return a.store.UpdateItemStatus(ctx, itemID, core.StatusRejected)
}

// issueBody renders the issue markdown. Content was scrubbed at triage time;
// we scrub again here as defense-in-depth before anything leaves for GitHub.
func issueBody(item core.Item) string {
	body, _ := scrub.Text(item.ScrubbedBody)
	summary, _ := scrub.Text(item.Summary)

	var b strings.Builder
	fmt.Fprintf(&b, "**Category:** %s\n\n", item.Category)
	if summary != "" {
		fmt.Fprintf(&b, "**Summary:** %s\n\n", summary)
	}
	b.WriteString("**Feedback (scrubbed):**\n\n")
	b.WriteString("> ")
	b.WriteString(strings.ReplaceAll(body, "\n", "\n> "))
	b.WriteString("\n\n---\n")
	fmt.Fprintf(&b, "_Filed by Earshot after human triage · %d report(s) clustered._\n", item.FeedbackCount)
	return b.String()
}
