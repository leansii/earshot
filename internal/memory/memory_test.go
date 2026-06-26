// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"context"
	"testing"

	"github.com/leansii/earshot/internal/core"
	"github.com/leansii/earshot/internal/queue"
)

// TestTx_CommitDeliversBoth verifies the transactional-outbox happy path: after
// commit, the feedback is queryable and exactly one job is delivered.
func TestTx_CommitDeliversBoth(t *testing.T) {
	ctx := context.Background()
	b := New()

	tx, err := b.BeginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := b.CreateFeedback(ctx, tx, core.Feedback{ProjectID: "p1", Body: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Enqueue(ctx, tx, queue.Job{Kind: queue.KindEnrich, FeedbackID: fb.ID}); err != nil {
		t.Fatal(err)
	}

	// Before commit: nothing is visible or delivered.
	if _, err := b.Feedback(ctx, fb.ID); err == nil {
		t.Fatal("feedback must not be visible before commit")
	}
	if jobs := b.TakeJobs(); len(jobs) != 0 {
		t.Fatalf("no jobs before commit, got %d", len(jobs))
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Feedback(ctx, fb.ID); err != nil {
		t.Fatalf("feedback should be visible after commit: %v", err)
	}
	if jobs := b.TakeJobs(); len(jobs) != 1 {
		t.Fatalf("expected 1 job after commit, got %d", len(jobs))
	}
}

// TestTx_RollbackDeliversNeither verifies that an aborted transaction leaves no
// feedback and no job.
func TestTx_RollbackDeliversNeither(t *testing.T) {
	ctx := context.Background()
	b := New()

	tx, _ := b.BeginTx(ctx)
	fb, _ := b.CreateFeedback(ctx, tx, core.Feedback{ProjectID: "p1", Body: "hi"})
	_ = b.Enqueue(ctx, tx, queue.Job{Kind: queue.KindEnrich, FeedbackID: fb.ID})

	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Feedback(ctx, fb.ID); err == nil {
		t.Fatal("feedback must not exist after rollback")
	}
	if jobs := b.TakeJobs(); len(jobs) != 0 {
		t.Fatalf("no jobs after rollback, got %d", len(jobs))
	}
}
