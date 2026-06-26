// SPDX-License-Identifier: Apache-2.0

package ghissue

import (
	"context"
	"fmt"

	"github.com/google/go-github/v66/github"
)

// Client is the real Issuer backed by google/go-github. It builds a fresh,
// token-scoped client per call so a project's PAT is never shared across
// tenants.
type Client struct{}

// NewClient returns a GitHub-backed Issuer.
func NewClient() *Client { return &Client{} }

// CreateIssue implements Issuer.
func (c *Client) CreateIssue(ctx context.Context, token string, req IssueRequest) (IssueResult, error) {
	if token == "" {
		return IssueResult{}, fmt.Errorf("ghissue: empty project token")
	}
	gh := github.NewClient(nil).WithAuthToken(token)

	issue, _, err := gh.Issues.Create(ctx, req.Owner, req.Repo, &github.IssueRequest{
		Title:  github.String(req.Title),
		Body:   github.String(req.Body),
		Labels: &req.Labels,
	})
	if err != nil {
		return IssueResult{}, fmt.Errorf("ghissue: create issue: %w", err)
	}
	return IssueResult{Number: issue.GetNumber(), URL: issue.GetHTMLURL()}, nil
}
