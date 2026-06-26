// SPDX-License-Identifier: Apache-2.0

// Package queue defines the async job boundary. The HTTP path only ever
// enqueues; all LLM and GitHub work happens off-request in a worker.
//
// The Queue interface is the seam that keeps the backend swappable: River
// (Postgres-backed) is the intended production driver, and an in-memory
// implementation backs dev and the hermetic test suite. Moving to another
// backend (e.g. Redis/asynq) is implementing this interface, not a rewrite.
package queue

import (
	"context"

	"github.com/leansii/earshot/internal/core"
)

// Kind identifies a job type.
type Kind string

const (
	// KindEnrich runs the full pipeline for one feedback row:
	// enrich -> dedup -> scrub -> triage (draft in needs_triage).
	KindEnrich Kind = "enrich"
)

// Job is a unit of async work. Payloads are small and reference rows by ID so
// jobs stay idempotent and safe to retry.
type Job struct {
	Kind       Kind   `json:"kind"`
	FeedbackID string `json:"feedback_id"`
}

// Queue is the enqueue side of the boundary.
//
// Enqueue MUST honor transactional-outbox semantics: when tx is non-nil the
// job is inserted in the same transaction as the originating DB write, so a
// committed feedback row always has a matching job and a rolled-back one never
// leaks a job. When tx is nil the enqueue is standalone (e.g. a manual
// re-drive).
type Queue interface {
	Enqueue(ctx context.Context, tx core.Tx, job Job) error
}

// Handler processes a single job. Returning an error signals the backend to
// retry according to its policy.
type Handler func(ctx context.Context, job Job) error

// Consumer is the worker side. Not every backend exposes consumption the same
// way (River registers typed workers; the in-memory queue runs a loop), so it
// is a separate interface from Queue.
type Consumer interface {
	// Run consumes jobs, dispatching each to h, until ctx is cancelled.
	Run(ctx context.Context, h Handler) error
}
