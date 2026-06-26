// SPDX-License-Identifier: Apache-2.0

package ghissue

import (
	"context"
	"sync"
)

// Mock is an in-memory Issuer for tests. It records every call so a test can
// assert that NOTHING reaches GitHub without an explicit approval, and that an
// approval files exactly one scrubbed issue.
type Mock struct {
	mu     sync.Mutex
	Calls  []IssueRequest
	Tokens []string
	// Result is returned from CreateIssue; defaults to issue #1.
	Result IssueResult
	// Err, if set, is returned from CreateIssue.
	Err error
}

// NewMock returns a Mock that reports issue #1 by default.
func NewMock() *Mock {
	return &Mock{Result: IssueResult{Number: 1, URL: "https://github.com/example/repo/issues/1"}}
}

// CreateIssue implements Issuer.
func (m *Mock) CreateIssue(_ context.Context, token string, req IssueRequest) (IssueResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return IssueResult{}, m.Err
	}
	m.Calls = append(m.Calls, req)
	m.Tokens = append(m.Tokens, token)
	return m.Result, nil
}

// CallCount returns how many issues were filed.
func (m *Mock) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Calls)
}
