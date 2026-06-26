// SPDX-License-Identifier: Apache-2.0

package connector

import (
	"context"
	"fmt"

	"github.com/google/go-github/v66/github"
)

// GitHub is the GitHub-backed Tracker. It builds a fresh, token-scoped client
// per call so a project's PAT is never shared across tenants.
type GitHub struct{}

// NewGitHub returns a GitHub Tracker.
func NewGitHub() *GitHub { return &GitHub{} }

// Provider implements Tracker.
func (*GitHub) Provider() string { return "github" }

func client(token string) (*github.Client, error) {
	if token == "" {
		return nil, fmt.Errorf("connector/github: empty project token")
	}
	return github.NewClient(nil).WithAuthToken(token), nil
}

// CreateIssue implements Tracker.
func (*GitHub) CreateIssue(ctx context.Context, token string, req IssueRequest) (IssueRef, error) {
	gh, err := client(token)
	if err != nil {
		return IssueRef{}, err
	}
	labels := req.Labels
	issue, _, err := gh.Issues.Create(ctx, req.Owner, req.Repo, &github.IssueRequest{
		Title:  github.String(req.Title),
		Body:   github.String(req.Body),
		Labels: &labels,
	})
	if err != nil {
		return IssueRef{}, fmt.Errorf("connector/github: create issue: %w", err)
	}
	return refFromIssue(req.Owner, req.Repo, issue), nil
}

// CreateSubIssue implements Tracker: create the child, then link it under the
// parent via the sub-issues API.
func (g *GitHub) CreateSubIssue(ctx context.Context, token string, parent IssueRef, req IssueRequest) (IssueRef, error) {
	if req.Owner == "" {
		req.Owner = parent.Owner
	}
	if req.Repo == "" {
		req.Repo = parent.Repo
	}
	child, err := g.CreateIssue(ctx, token, req)
	if err != nil {
		return IssueRef{}, err
	}
	gh, err := client(token)
	if err != nil {
		return IssueRef{}, err
	}
	// The sub-issues endpoint is addressed via the parent number and links the
	// child by its database id. Issued as a raw request for forward-compat with
	// the go-github version in use.
	url := fmt.Sprintf("repos/%s/%s/issues/%d/sub_issues", parent.Owner, parent.Repo, parent.Number)
	httpReq, err := gh.NewRequest("POST", url, map[string]int64{"sub_issue_id": child.ID})
	if err != nil {
		return child, fmt.Errorf("connector/github: build sub-issue link: %w", err)
	}
	if _, err := gh.Do(ctx, httpReq, nil); err != nil {
		// The child issue exists; surface the linking error so the caller can
		// decide whether to retry the link.
		return child, fmt.Errorf("connector/github: link sub-issue #%d under #%d: %w", child.Number, parent.Number, err)
	}
	return child, nil
}

// AddComment implements Tracker.
func (*GitHub) AddComment(ctx context.Context, token string, issue IssueRef, body string) (IssueRef, error) {
	gh, err := client(token)
	if err != nil {
		return IssueRef{}, err
	}
	c, _, err := gh.Issues.CreateComment(ctx, issue.Owner, issue.Repo, issue.Number, &github.IssueComment{
		Body: github.String(body),
	})
	if err != nil {
		return IssueRef{}, fmt.Errorf("connector/github: add comment: %w", err)
	}
	return IssueRef{Owner: issue.Owner, Repo: issue.Repo, Number: issue.Number, ID: c.GetID(), URL: c.GetHTMLURL()}, nil
}

// UpdateIssueBody implements Tracker.
func (*GitHub) UpdateIssueBody(ctx context.Context, token string, issue IssueRef, body string) error {
	gh, err := client(token)
	if err != nil {
		return err
	}
	_, _, err = gh.Issues.Edit(ctx, issue.Owner, issue.Repo, issue.Number, &github.IssueRequest{
		Body: github.String(body),
	})
	if err != nil {
		return fmt.Errorf("connector/github: update issue body: %w", err)
	}
	return nil
}

func refFromIssue(owner, repo string, issue *github.Issue) IssueRef {
	return IssueRef{
		Owner:  owner,
		Repo:   repo,
		Number: issue.GetNumber(),
		ID:     issue.GetID(),
		NodeID: issue.GetNodeID(),
		URL:    issue.GetHTMLURL(),
	}
}
