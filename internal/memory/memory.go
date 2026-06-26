// SPDX-License-Identifier: Apache-2.0

// Package memory is an in-memory implementation of both store.Store and
// queue.Queue, sharing one lock so the transactional-outbox guarantee can be
// demonstrated and tested without a database. It backs the hermetic test suite
// and `earshot ... --queue=memory` for local dev.
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/leansii/earshot/internal/core"
	"github.com/leansii/earshot/internal/queue"
	"github.com/leansii/earshot/internal/store"
)

// Backend holds all state behind a single mutex.
type Backend struct {
	mu sync.Mutex

	projects map[string]core.Project
	keys     map[string]core.AppKey // by hash
	feedback map[string]core.Feedback
	items    map[string]core.Item
	tasks    map[string]core.Task
	fbToItem map[string]string // feedbackID -> itemID

	jobs chan queue.Job // committed jobs awaiting consumption

	seq int // monotonic id source (deterministic for tests)
	now func() time.Time
}

// New returns an empty Backend.
func New() *Backend {
	return &Backend{
		projects: map[string]core.Project{},
		keys:     map[string]core.AppKey{},
		feedback: map[string]core.Feedback{},
		items:    map[string]core.Item{},
		tasks:    map[string]core.Task{},
		fbToItem: map[string]string{},
		jobs:     make(chan queue.Job, 1024),
		now:      time.Now,
	}
}

// compile-time interface checks.
var (
	_ store.Store    = (*Backend)(nil)
	_ queue.Queue    = (*Backend)(nil)
	_ queue.Consumer = (*Backend)(nil)
)

func (b *Backend) nextID(prefix string) string {
	b.seq++
	return fmt.Sprintf("%s_%d", prefix, b.seq)
}

// --- Transaction ---

// memTx buffers pending writes so feedback insertion and its job enqueue commit
// atomically.
type memTx struct {
	b           *Backend
	pendingFeed []core.Feedback
	pendingJobs []queue.Job
	done        bool
}

// BeginTx implements store.Store.
func (b *Backend) BeginTx(_ context.Context) (core.Tx, error) {
	return &memTx{b: b}, nil
}

// Commit applies buffered writes under the backend lock.
func (t *memTx) Commit(_ context.Context) error {
	if t.done {
		return fmt.Errorf("memory: tx already finished")
	}
	t.done = true
	t.b.mu.Lock()
	for _, fb := range t.pendingFeed {
		t.b.feedback[fb.ID] = fb
	}
	t.b.mu.Unlock()
	// Deliver jobs only after the lock is released and rows are durable.
	for _, j := range t.pendingJobs {
		t.b.jobs <- j
	}
	return nil
}

// Rollback discards buffered writes.
func (t *memTx) Rollback(_ context.Context) error {
	t.done = true
	t.pendingFeed = nil
	t.pendingJobs = nil
	return nil
}

// --- queue.Queue ---

// Enqueue implements queue.Queue. With a *memTx the job is buffered until
// commit; otherwise it is delivered immediately.
func (b *Backend) Enqueue(_ context.Context, tx core.Tx, job queue.Job) error {
	if tx == nil {
		b.jobs <- job
		return nil
	}
	mt, ok := tx.(*memTx)
	if !ok {
		return fmt.Errorf("memory: foreign tx type %T", tx)
	}
	mt.pendingJobs = append(mt.pendingJobs, job)
	return nil
}

// --- queue.Consumer ---

// Run consumes committed jobs until ctx is cancelled.
func (b *Backend) Run(ctx context.Context, h queue.Handler) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case job := <-b.jobs:
			if err := h(ctx, job); err != nil {
				// In-memory backend has no retry; surface via the handler's
				// own logging. A real backend (River) retries with backoff.
				_ = err
			}
		}
	}
}

// TakeJobs drains and returns all currently-enqueued jobs without blocking.
// Tests use this to drive the pipeline deterministically.
func (b *Backend) TakeJobs() []queue.Job {
	var out []queue.Job
	for {
		select {
		case j := <-b.jobs:
			out = append(out, j)
		default:
			return out
		}
	}
}

// --- store.Store: auth ---

// AppKeyByHash implements store.Store.
func (b *Backend) AppKeyByHash(_ context.Context, keyHash string) (core.AppKey, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	k, ok := b.keys[keyHash]
	if !ok || k.RevokedAt != nil {
		return core.AppKey{}, store.ErrNotFound
	}
	return k, nil
}

// Project implements store.Store.
func (b *Backend) Project(_ context.Context, id string) (core.Project, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.projects[id]
	if !ok {
		return core.Project{}, store.ErrNotFound
	}
	return p, nil
}

// --- store.Store: ingest ---

// CreateFeedback implements store.Store. The row is buffered in tx until commit.
func (b *Backend) CreateFeedback(_ context.Context, tx core.Tx, fb core.Feedback) (core.Feedback, error) {
	mt, ok := tx.(*memTx)
	if !ok {
		return core.Feedback{}, fmt.Errorf("memory: foreign tx type %T", tx)
	}
	b.mu.Lock()
	fb.ID = b.nextID("fb")
	fb.CreatedAt = b.now()
	b.mu.Unlock()
	mt.pendingFeed = append(mt.pendingFeed, fb)
	return fb, nil
}

// --- store.Store: pipeline ---

// Feedback implements store.Store.
func (b *Backend) Feedback(_ context.Context, id string) (core.Feedback, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fb, ok := b.feedback[id]
	if !ok {
		return core.Feedback{}, store.ErrNotFound
	}
	return fb, nil
}

// ItemsForDedup implements store.Store.
func (b *Backend) ItemsForDedup(_ context.Context, projectID string) ([]core.Item, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []core.Item
	for _, it := range b.items {
		if it.ProjectID == projectID && len(it.Embedding) > 0 {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// CreateItem implements store.Store.
func (b *Backend) CreateItem(_ context.Context, item core.Item) (core.Item, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	item.ID = b.nextID("item")
	now := b.now()
	item.CreatedAt = now
	item.UpdatedAt = now
	if item.Status == "" {
		item.Status = core.StatusNeedsTriage
	}
	b.items[item.ID] = item
	return item, nil
}

// LinkFeedback implements store.Store.
func (b *Backend) LinkFeedback(_ context.Context, feedbackID, itemID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	it, ok := b.items[itemID]
	if !ok {
		return store.ErrNotFound
	}
	b.fbToItem[feedbackID] = itemID
	it.FeedbackCount++
	it.UpdatedAt = b.now()
	b.items[itemID] = it
	return nil
}

// --- store.Store: triage ---

// Item implements store.Store.
func (b *Backend) Item(_ context.Context, id string) (core.Item, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	it, ok := b.items[id]
	if !ok {
		return core.Item{}, store.ErrNotFound
	}
	return it, nil
}

// ListItems implements store.Store.
func (b *Backend) ListItems(_ context.Context, projectID string, status core.Status) ([]core.Item, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []core.Item
	for _, it := range b.items {
		if it.ProjectID == projectID && (status == "" || it.Status == status) {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// UpdateItemStatus implements store.Store.
func (b *Backend) UpdateItemStatus(_ context.Context, id string, status core.Status) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	it, ok := b.items[id]
	if !ok {
		return store.ErrNotFound
	}
	it.Status = status
	it.UpdatedAt = b.now()
	b.items[id] = it
	return nil
}

// MarkItemFiled implements store.Store.
func (b *Backend) MarkItemFiled(_ context.Context, id string, issueNumber int, issueURL string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	it, ok := b.items[id]
	if !ok {
		return store.ErrNotFound
	}
	it.Status = core.StatusFiled
	it.GitHubIssueNumber = issueNumber
	it.GitHubIssueURL = issueURL
	it.UpdatedAt = b.now()
	b.items[id] = it
	return nil
}

// --- store.Store: tasks ---

// CreateTask implements store.Store.
func (b *Backend) CreateTask(_ context.Context, task core.Task) (core.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	task.ID = b.nextID("task")
	now := b.now()
	task.CreatedAt = now
	task.UpdatedAt = now
	if task.Status == "" {
		task.Status = core.StatusNeedsTriage
	}
	b.tasks[task.ID] = task
	return task, nil
}

// Task implements store.Store.
func (b *Backend) Task(_ context.Context, id string) (core.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tasks[id]
	if !ok {
		return core.Task{}, store.ErrNotFound
	}
	return t, nil
}

// TasksForItem implements store.Store.
func (b *Backend) TasksForItem(_ context.Context, itemID string) ([]core.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []core.Task
	for _, t := range b.tasks {
		if t.ItemID == itemID {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ListTasks implements store.Store.
func (b *Backend) ListTasks(_ context.Context, projectID string, status core.Status) ([]core.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []core.Task
	for _, t := range b.tasks {
		if t.ProjectID == projectID && (status == "" || t.Status == status) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// UpdateTaskStatus implements store.Store.
func (b *Backend) UpdateTaskStatus(_ context.Context, id string, status core.Status) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tasks[id]
	if !ok {
		return store.ErrNotFound
	}
	t.Status = status
	t.UpdatedAt = b.now()
	b.tasks[id] = t
	return nil
}

// MarkTaskPushed implements store.Store.
func (b *Backend) MarkTaskPushed(_ context.Context, id, ref string, number int, url string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tasks[id]
	if !ok {
		return store.ErrNotFound
	}
	t.Status = core.StatusFiled
	t.TrackerRef = ref
	t.TrackerNumber = number
	t.TrackerURL = url
	t.UpdatedAt = b.now()
	b.tasks[id] = t
	return nil
}

// --- store.Store: admin ---

// CreateProject implements store.Store.
func (b *Backend) CreateProject(_ context.Context, p core.Project) (core.Project, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if p.ID == "" {
		p.ID = b.nextID("proj")
	}
	p.ApplyDefaults()
	p.CreatedAt = b.now()
	b.projects[p.ID] = p
	return p, nil
}

// CreateAppKey implements store.Store.
func (b *Backend) CreateAppKey(_ context.Context, k core.AppKey) (core.AppKey, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if k.ID == "" {
		k.ID = b.nextID("key")
	}
	k.CreatedAt = b.now()
	b.keys[k.KeyHash] = k
	return k, nil
}
