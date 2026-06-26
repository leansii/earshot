// SPDX-License-Identifier: Apache-2.0

// Package ghissue is the GitHub issue-creation adapter. It is the ONLY place
// Earshot writes to GitHub, and it is invoked exclusively from an explicit
// human approve action — never from the enrichment pipeline.
//
// Least privilege: the token is passed per call (a per-project fine-grained PAT
// scoped to a single repo with "Issues: read & write"), never a long-lived
// org-wide credential held on a shared client.
package ghissue

import "context"

// IssueRequest is a draft issue to file.
type IssueRequest struct {
	Owner  string
	Repo   string
	Title  string
	Body   string
	Labels []string
}

// IssueResult identifies the created issue.
type IssueResult struct {
	Number int
	URL    string
}

// Issuer creates GitHub issues. token is the per-project fine-grained PAT.
type Issuer interface {
	CreateIssue(ctx context.Context, token string, req IssueRequest) (IssueResult, error)
}
