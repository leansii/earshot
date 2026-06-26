// SPDX-License-Identifier: Apache-2.0

// Package core holds Earshot's shared domain types. It deliberately depends on
// nothing else in the project so every other package can import it without
// creating cycles.
package core

import (
	"context"
	"time"
)

// Status is the lifecycle state of a triage item (a "draft").
//
// The pipeline only ever produces NeedsTriage. A human moves it to Approved or
// Rejected; only an Approved item may be Filed to GitHub. Nothing in the
// enrichment pipeline is permitted to auto-advance past NeedsTriage.
type Status string

const (
	StatusNeedsTriage Status = "needs_triage"
	StatusApproved    Status = "approved"
	StatusRejected    Status = "rejected"
	StatusFiled       Status = "filed"
)

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	switch s {
	case StatusNeedsTriage, StatusApproved, StatusRejected, StatusFiled:
		return true
	default:
		return false
	}
}

// Project is a single tenant: one GitHub repository that receives triaged
// issues, plus the per-project secrets and model configuration. Tenant
// isolation is enforced by always scoping queries to a ProjectID.
type Project struct {
	ID          string
	Slug        string
	Name        string
	GitHubOwner string
	GitHubRepo  string
	// GitHubToken is a fine-grained PAT scoped to exactly this repo with
	// "Issues: read & write". It is a secret: never logged, never serialized
	// to API responses (note the json:"-" tag).
	GitHubToken string `json:"-"`
	Labels      []string
	// LLMModel and EmbedModel are addressed as provider:model.
	LLMModel   string
	EmbedModel string
	// AutoFile is OFF by default and intentionally unused by the v1 pipeline.
	// It exists only so the schema is ready for a future, explicitly opted-in
	// auto-file mode guarded by a confidence threshold. See SECURITY.md.
	AutoFile  bool
	CreatedAt time.Time
}

// AppKey is a per-app API credential. A key maps to exactly one project; a key
// can never reach another project's data. Only KeyHash is stored — the raw key
// is shown once at creation time.
type AppKey struct {
	ID        string
	ProjectID string
	Name      string
	// KeyHash is the hex SHA-256 of the raw API key.
	KeyHash string `json:"-"`
	// Prefix is a short, non-secret identifier shown in dashboards/logs.
	Prefix          string
	RateLimitPerMin int
	CreatedAt       time.Time
	RevokedAt       *time.Time
}

// Feedback is one raw submission from an integrating app. Body is untrusted
// user input and is treated strictly as data, never as instructions.
type Feedback struct {
	ID        string
	ProjectID string
	AppID     string
	Version   string
	UserRef   string
	Route     string
	Severity  string
	Body      string
	// Context is arbitrary structured metadata supplied by the app (JSON).
	Context   map[string]any
	CreatedAt time.Time
}

// Item is a deduplicated cluster of feedback and the unit a human triages. The
// pipeline writes it as a draft in NeedsTriage; approval files it to GitHub.
type Item struct {
	ID        string
	ProjectID string
	Category  string
	Title     string
	Summary   string
	// ScrubbedBody is the representative feedback body after PII/secret
	// redaction — this is what would appear in a (possibly public) issue.
	ScrubbedBody string
	Status       Status
	// Embedding is the dedup vector for the representative feedback.
	Embedding     []float32
	FeedbackCount int
	// GitHub linkage, populated only after an approved item is filed.
	GitHubIssueNumber int
	GitHubIssueURL    string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Tx is an opaque unit-of-work handle. It lets the ingest path write feedback
// and enqueue its enrichment job atomically (the transactional-outbox
// pattern). Concrete implementations live in the store/queue adapters; callers
// only Commit or Rollback.
type Tx interface {
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}
