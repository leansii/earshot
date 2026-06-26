// SPDX-License-Identifier: Apache-2.0

// Package store defines the persistence boundary. Keeping it an interface lets
// the unit tests run against a fast in-memory implementation while production
// uses Postgres (pgx); a thin integration test can exercise the Postgres path.
package store

import (
	"context"
	"errors"

	"github.com/leansii/earshot/internal/core"
)

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("store: not found")

// Store is the full persistence surface used by Earshot. All item/feedback
// methods are scoped by ProjectID to preserve tenant isolation.
type Store interface {
	// BeginTx starts a unit of work usable by both the store and the queue,
	// enabling transactional-outbox enqueue.
	BeginTx(ctx context.Context) (core.Tx, error)

	// --- Auth ---

	// AppKeyByHash resolves a hashed API key to its key record. Returns
	// ErrNotFound if missing or revoked.
	AppKeyByHash(ctx context.Context, keyHash string) (core.AppKey, error)
	// Project fetches a project (including its secrets) by ID.
	Project(ctx context.Context, id string) (core.Project, error)

	// --- Ingest (transactional) ---

	// CreateFeedback inserts a feedback row inside tx.
	CreateFeedback(ctx context.Context, tx core.Tx, fb core.Feedback) (core.Feedback, error)

	// --- Pipeline ---

	Feedback(ctx context.Context, id string) (core.Feedback, error)
	// ItemsForDedup returns existing items in a project that are candidates
	// for near-duplicate comparison (those with an embedding).
	ItemsForDedup(ctx context.Context, projectID string) ([]core.Item, error)
	CreateItem(ctx context.Context, item core.Item) (core.Item, error)
	// LinkFeedback attaches a feedback row to an item and bumps its count.
	LinkFeedback(ctx context.Context, feedbackID, itemID string) error

	// --- Triage / approval ---

	Item(ctx context.Context, id string) (core.Item, error)
	ListItems(ctx context.Context, projectID string, status core.Status) ([]core.Item, error)
	UpdateItemStatus(ctx context.Context, id string, status core.Status) error
	// MarkItemFiled records the tracker issue created for a feedback-issue.
	MarkItemFiled(ctx context.Context, id string, issueNumber int, issueURL string) error

	// --- Tasks (derived by the triage-agent; human-gated) ---

	CreateTask(ctx context.Context, task core.Task) (core.Task, error)
	Task(ctx context.Context, id string) (core.Task, error)
	// TasksForItem returns all tasks attached to a feedback-issue, oldest first.
	TasksForItem(ctx context.Context, itemID string) ([]core.Task, error)
	// ListTasks returns a project's tasks filtered by status ("" = all).
	ListTasks(ctx context.Context, projectID string, status core.Status) ([]core.Task, error)
	UpdateTaskStatus(ctx context.Context, id string, status core.Status) error
	// MarkTaskPushed records the tracker reference for an approved, pushed task.
	MarkTaskPushed(ctx context.Context, id, ref string, number int, url string) error

	// --- Admin / seeding (used by the CLI and tests) ---

	CreateProject(ctx context.Context, p core.Project) (core.Project, error)
	CreateAppKey(ctx context.Context, k core.AppKey) (core.AppKey, error)
}
