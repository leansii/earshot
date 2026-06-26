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

// TrackerProvider selects the external issue tracker a project pushes to.
type TrackerProvider string

const (
	TrackerGitHub TrackerProvider = "github"
	// TrackerNone keeps everything in Earshot's DB and pushes nowhere. The DB
	// is always the source of truth; the tracker is a projection.
	TrackerNone TrackerProvider = "none"
)

// TaskStrategy controls how approved tasks are attached to their parent
// feedback-issue in the tracker.
type TaskStrategy string

const (
	// StrategySubIssues files each task as a native child issue (GitHub
	// sub-issues) linked to the feedback-issue.
	StrategySubIssues TaskStrategy = "sub_issues"
	// StrategyChecklist renders tasks as a markdown task-list in the
	// feedback-issue body.
	StrategyChecklist TaskStrategy = "checklist"
	// StrategyComments posts each task as a comment on the feedback-issue.
	StrategyComments TaskStrategy = "comments"
)

// Valid reports whether s is a known task strategy.
func (s TaskStrategy) Valid() bool {
	switch s {
	case StrategySubIssues, StrategyChecklist, StrategyComments:
		return true
	default:
		return false
	}
}

// Project is a single tenant: one tracker repository that receives feedback-
// issues, plus the per-project secrets, model, and connector configuration.
// Tenant isolation is enforced by always scoping queries to a ProjectID.
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

	// --- Connector configuration (see the README "configuration" table) ---

	// Tracker is which external system feedback-issues are pushed to.
	Tracker TrackerProvider
	// FeedbackAutoFile, when true, auto-creates the feedback-issue in the
	// tracker after scrubbing (no human gate at the issue level). The human
	// gate lives at the task level instead.
	FeedbackAutoFile bool
	// TaskStrategy is how approved tasks attach to the feedback-issue.
	TaskStrategy TaskStrategy
	// ConnectorConfig holds provider-specific settings (e.g. Jira project key)
	// so new connectors need no schema change.
	ConnectorConfig map[string]any

	CreatedAt time.Time
}

// ApplyDefaults fills unset connector/model fields with sane defaults. It does
// not touch FeedbackAutoFile (a bool whose intended default is true but which
// the caller sets explicitly, so an intentional false is preserved).
func (p *Project) ApplyDefaults() {
	if p.Tracker == "" {
		p.Tracker = TrackerGitHub
	}
	if p.TaskStrategy == "" {
		p.TaskStrategy = StrategySubIssues
	}
	if p.LLMModel == "" {
		p.LLMModel = "fake:classifier-v1"
	}
	if p.EmbedModel == "" {
		p.EmbedModel = "fake:hash-256"
	}
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

// Item is a deduplicated cluster of feedback — the "feedback-issue". With
// FeedbackAutoFile on, the pipeline scrubs it and files it to the tracker
// automatically (Status becomes Filed). The triage-agent then decomposes it
// into Tasks, which are the human-gated unit.
type Item struct {
	ID        string
	ProjectID string
	Category  string
	Title     string
	Summary   string
	// ScrubbedBody is the representative feedback body after PII/secret
	// redaction — this is what appears in a (possibly public) issue. The
	// pipeline refuses to file a feedback-issue if scrubbing has not run.
	ScrubbedBody string
	Status       Status
	// Embedding is the dedup vector for the representative feedback.
	Embedding     []float32
	FeedbackCount int
	// Tracker linkage, populated when the feedback-issue is filed.
	GitHubIssueNumber int
	GitHubIssueURL    string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Task is an actionable work item that the triage-agent derives from a
// feedback-issue (Item). Tasks always live in Earshot's DB first (the source of
// truth). A task starts in NeedsTriage; a human Approves it; only then is it
// pushed to the tracker (Status Filed) and linked to its parent feedback-issue
// per the project's TaskStrategy. Rejecting never touches the tracker.
type Task struct {
	ID        string
	ProjectID string
	// ItemID is the parent feedback-issue.
	ItemID      string
	Title       string
	Description string
	Status      Status
	// Tracker linkage, populated once an approved task is pushed.
	TrackerRef    string // human-readable, e.g. "#42" or a comment URL
	TrackerNumber int
	TrackerURL    string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Tx is an opaque unit-of-work handle. It lets the ingest path write feedback
// and enqueue its enrichment job atomically (the transactional-outbox
// pattern). Concrete implementations live in the store/queue adapters; callers
// only Commit or Rollback.
type Tx interface {
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}
