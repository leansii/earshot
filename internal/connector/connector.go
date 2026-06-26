// SPDX-License-Identifier: Apache-2.0

// Package connector is the pluggable boundary to external issue trackers. A
// feedback-issue and its tasks always live in Earshot's database first; a
// connector projects them into a tracker (GitHub today; Jira/Linear later).
//
// It exposes small primitives (create issue, create sub-issue, comment, update
// body); the task-attachment STRATEGY (sub-issues vs checklist vs comments) is
// applied a layer up, in package triage, so strategy logic is provider-neutral.
//
// Least privilege: the per-project token is passed per call, never held on a
// shared client.
package connector

import "context"

// IssueRef identifies an issue in a tracker. Owner/Repo/Number/ID are carried so
// follow-up calls (sub-issues, comments, edits) need no extra lookup.
type IssueRef struct {
	Owner  string
	Repo   string
	Number int
	// ID is the tracker's internal database id (GitHub needs it to link a
	// sub-issue to its parent).
	ID     int64
	NodeID string
	URL    string
}

// IssueRequest is a draft issue to create.
type IssueRequest struct {
	Owner  string
	Repo   string
	Title  string
	Body   string
	Labels []string
}

// Tracker is the set of primitives a connector must implement.
type Tracker interface {
	// Provider returns the provider id (e.g. "github").
	Provider() string
	// CreateIssue files a top-level issue (the feedback-issue).
	CreateIssue(ctx context.Context, token string, req IssueRequest) (IssueRef, error)
	// CreateSubIssue files a child issue and links it under parent.
	CreateSubIssue(ctx context.Context, token string, parent IssueRef, req IssueRequest) (IssueRef, error)
	// AddComment posts a comment on an issue and returns a ref to it.
	AddComment(ctx context.Context, token string, issue IssueRef, body string) (IssueRef, error)
	// UpdateIssueBody replaces an issue's body (used by the checklist strategy).
	UpdateIssueBody(ctx context.Context, token string, issue IssueRef, body string) error
}
