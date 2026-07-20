// SPDX-License-Identifier: Apache-2.0

// Package riverq is the River (Postgres-backed) implementation of queue.Queue.
// It is the intended production driver: River's InsertTx enqueues a job in the
// same pgx transaction as the feedback insert, giving exact transactional-
// outbox semantics. Swapping to another backend means implementing the same
// queue.Queue interface — not rewriting callers.
//
// River maintains its own tables; run its migrations once against the database
// (see README) before starting a worker.
package riverq

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/leansii/earshot/internal/core"
	"github.com/leansii/earshot/internal/queue"
)

// EnrichArgs is the River job payload for the enrich pipeline.
type EnrichArgs struct {
	FeedbackID string `json:"feedback_id"`
}

// Kind implements river.JobArgs.
func (EnrichArgs) Kind() string { return string(queue.KindEnrich) }

// pgxTxProvider is satisfied by the pgxstore transaction wrapper, letting us
// enqueue in the caller's transaction without importing the store package.
type pgxTxProvider interface{ PgxTx() pgx.Tx }

// Queue is the River-backed enqueue side (insert-only client).
type Queue struct {
	client *river.Client[pgx.Tx]
}

var _ queue.Queue = (*Queue)(nil)

// NewQueue builds an insert-only River client over the given pool.
func NewQueue(pool *pgxpool.Pool) (*Queue, error) {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return nil, fmt.Errorf("riverq: new client: %w", err)
	}
	return &Queue{client: client}, nil
}

// Enqueue implements queue.Queue with transactional-outbox semantics.
func (q *Queue) Enqueue(ctx context.Context, tx core.Tx, job queue.Job) error {
	args := EnrichArgs{FeedbackID: job.FeedbackID}
	if tx == nil {
		_, err := q.client.Insert(ctx, args, nil)
		return err
	}
	p, ok := tx.(pgxTxProvider)
	if !ok {
		return fmt.Errorf("riverq: tx %T does not expose a pgx.Tx", tx)
	}
	if _, err := q.client.InsertTx(ctx, p.PgxTx(), args, nil); err != nil {
		return fmt.Errorf("riverq: insert tx: %w", err)
	}
	return nil
}

// enrichWorker adapts a queue.Handler to a River worker.
type enrichWorker struct {
	river.WorkerDefaults[EnrichArgs]
	handler queue.Handler
}

// Work implements river.Worker.
func (w *enrichWorker) Work(ctx context.Context, j *river.Job[EnrichArgs]) error {
	return w.handler(ctx, queue.Job{Kind: queue.KindEnrich, FeedbackID: j.Args.FeedbackID})
}

// Consumer runs River workers that dispatch to a queue.Handler.
type Consumer struct {
	client *river.Client[pgx.Tx]
	pool   *pgxpool.Pool
}

// NewConsumer builds a River client that consumes enrich jobs via h.
func NewConsumer(pool *pgxpool.Pool, h queue.Handler, maxWorkers int) (*Consumer, error) {
	if maxWorkers <= 0 {
		maxWorkers = 10
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, &enrichWorker{handler: h})

	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: maxWorkers},
		},
		Workers: workers,
	})
	if err != nil {
		return nil, fmt.Errorf("riverq: new consumer: %w", err)
	}
	return &Consumer{client: client, pool: pool}, nil
}

// Run starts River and blocks until ctx is cancelled.
func (c *Consumer) Run(ctx context.Context) error {
	if err := c.client.Start(ctx); err != nil {
		return fmt.Errorf("riverq: start: %w", err)
	}
	<-ctx.Done()
	return c.client.Stop(context.Background())
}

// runnableJobsSQL reports whether any job is runnable right now: waiting to
// be picked up, currently executing, or due for a retry. Retries scheduled in
// the future do not count — drain leaves them for the next run.
const runnableJobsSQL = `
SELECT EXISTS (
    SELECT 1 FROM river_job
    WHERE state IN ('available', 'running')
       OR (state IN ('retryable', 'scheduled') AND scheduled_at <= now())
)`

// RunDrain starts River, works jobs until none are runnable, then stops and
// returns. This is the scheduled-drain entry point: a cron-launched process
// empties the queue and exits, so the deployment (and a serverless database)
// can scale to zero between runs. In-flight jobs are finished before Stop
// returns; jobs whose retry lands in the future are left for the next drain.
func (c *Consumer) RunDrain(ctx context.Context, poll time.Duration) error {
	if poll <= 0 {
		poll = 2 * time.Second
	}
	if err := c.client.Start(ctx); err != nil {
		return fmt.Errorf("riverq: start: %w", err)
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	// Require two consecutive empty polls as a margin against transient gaps
	// between River's state transitions (fetch, completion flush).
	empty := 0
	for empty < 2 {
		select {
		case <-ctx.Done():
			return c.client.Stop(context.Background())
		case <-ticker.C:
		}
		var busy bool
		if err := c.pool.QueryRow(ctx, runnableJobsSQL).Scan(&busy); err != nil {
			_ = c.client.Stop(context.Background())
			return fmt.Errorf("riverq: drain poll: %w", err)
		}
		if busy {
			empty = 0
		} else {
			empty++
		}
	}
	return c.client.Stop(context.Background())
}
