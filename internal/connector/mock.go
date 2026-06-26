// SPDX-License-Identifier: Apache-2.0

package connector

import (
	"context"
	"fmt"
	"sync"
)

// Mock is an in-memory Tracker for tests. It records every primitive call so a
// test can assert exactly what was projected to the tracker and when.
type Mock struct {
	mu sync.Mutex

	Issues      []IssueRequest // CreateIssue calls (feedback-issues)
	SubIssues   []IssueRequest // CreateSubIssue calls
	Comments    []string       // AddComment bodies
	BodyUpdates []string       // UpdateIssueBody bodies
	Tokens      []string       // token seen on each call, in order

	// Err, if set, is returned by every primitive.
	Err error

	seq int
}

// NewMock returns an empty Mock tracker.
func NewMock() *Mock { return &Mock{} }

// Provider implements Tracker.
func (*Mock) Provider() string { return "mock" }

func (m *Mock) ref(owner, repo string) IssueRef {
	m.seq++
	return IssueRef{
		Owner:  owner,
		Repo:   repo,
		Number: m.seq,
		ID:     int64(1000 + m.seq),
		URL:    fmt.Sprintf("https://tracker.test/%s/%s/issues/%d", owner, repo, m.seq),
	}
}

// CreateIssue implements Tracker.
func (m *Mock) CreateIssue(_ context.Context, token string, req IssueRequest) (IssueRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return IssueRef{}, m.Err
	}
	m.Tokens = append(m.Tokens, token)
	m.Issues = append(m.Issues, req)
	return m.ref(req.Owner, req.Repo), nil
}

// CreateSubIssue implements Tracker.
func (m *Mock) CreateSubIssue(_ context.Context, token string, parent IssueRef, req IssueRequest) (IssueRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return IssueRef{}, m.Err
	}
	m.Tokens = append(m.Tokens, token)
	m.SubIssues = append(m.SubIssues, req)
	return m.ref(parent.Owner, parent.Repo), nil
}

// AddComment implements Tracker.
func (m *Mock) AddComment(_ context.Context, token string, issue IssueRef, body string) (IssueRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return IssueRef{}, m.Err
	}
	m.Tokens = append(m.Tokens, token)
	m.Comments = append(m.Comments, body)
	return m.ref(issue.Owner, issue.Repo), nil
}

// UpdateIssueBody implements Tracker.
func (m *Mock) UpdateIssueBody(_ context.Context, token string, _ IssueRef, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	m.Tokens = append(m.Tokens, token)
	m.BodyUpdates = append(m.BodyUpdates, body)
	return nil
}

// FeedbackIssueCount returns how many feedback-issues were filed.
func (m *Mock) FeedbackIssueCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Issues)
}

// TaskPushCount returns how many task projections happened (sub-issues +
// comments + body updates).
func (m *Mock) TaskPushCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.SubIssues) + len(m.Comments) + len(m.BodyUpdates)
}
