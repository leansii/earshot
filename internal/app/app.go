// SPDX-License-Identifier: Apache-2.0

// Package app wires Earshot's components together from configuration, choosing
// the in-memory backend for dev/tests or Postgres+River for production.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/leansii/earshot/internal/config"
	"github.com/leansii/earshot/internal/connector"
	"github.com/leansii/earshot/internal/embed"
	"github.com/leansii/earshot/internal/llm"
	"github.com/leansii/earshot/internal/memory"
	"github.com/leansii/earshot/internal/queue"
	"github.com/leansii/earshot/internal/queue/riverq"
	"github.com/leansii/earshot/internal/store"
	"github.com/leansii/earshot/internal/store/pgxstore"
	"github.com/leansii/earshot/internal/triage"
)

// App holds the wired components.
type App struct {
	Cfg      config.Config
	Store    store.Store
	Queue    queue.Queue
	Pipeline *triage.Pipeline
	Approver *triage.TaskApprover
	Log      *slog.Logger

	memBackend *memory.Backend
	pgStore    *pgxstore.Store
}

// Build constructs an App from configuration.
func Build(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	if log == nil {
		log = slog.Default()
	}

	provider, err := llm.New(cfg.LLMModel, cfg.KeyFunc())
	if err != nil {
		return nil, err
	}
	embedder, err := embed.New(cfg.EmbedModel)
	if err != nil {
		return nil, err
	}
	tracker := connector.NewGitHub()

	a := &App{Cfg: cfg, Log: log}

	switch cfg.Queue {
	case "memory":
		b := memory.New()
		a.memBackend = b
		a.Store = b
		a.Queue = b
	case "river":
		pg, err := pgxstore.New(ctx, cfg.DatabaseURL)
		if err != nil {
			return nil, err
		}
		q, err := riverq.NewQueue(pg.Pool())
		if err != nil {
			pg.Close()
			return nil, err
		}
		a.pgStore = pg
		a.Store = pg
		a.Queue = q
	default:
		return nil, fmt.Errorf("app: unknown queue backend %q", cfg.Queue)
	}

	a.Pipeline = triage.NewPipeline(a.Store, provider, embedder, tracker, 0, log)
	a.Approver = triage.NewTaskApprover(a.Store, tracker, log)
	return a, nil
}

// InMemory reports whether the app is using the single-process memory backend.
func (a *App) InMemory() bool { return a.memBackend != nil }

// RunWorker consumes jobs until ctx is cancelled, dispatching to the pipeline.
func (a *App) RunWorker(ctx context.Context) error {
	if a.memBackend != nil {
		return a.memBackend.Run(ctx, a.Pipeline.Handle)
	}
	consumer, err := riverq.NewConsumer(a.pgStore.Pool(), a.Pipeline.Handle, 10)
	if err != nil {
		return err
	}
	return consumer.Run(ctx)
}

// DrainWorker consumes queued jobs until none are runnable, then returns.
// This is the scheduled-drain mode (cron-launched process empties the queue
// and exits). River only — the memory queue lives inside `serve`.
func (a *App) DrainWorker(ctx context.Context) error {
	if a.memBackend != nil {
		return errors.New("app: drain requires the river queue; the memory queue is in-process only")
	}
	consumer, err := riverq.NewConsumer(a.pgStore.Pool(), a.Pipeline.Handle, 10)
	if err != nil {
		return err
	}
	return consumer.RunDrain(ctx, 2*time.Second)
}

// Close releases resources.
func (a *App) Close() {
	if a.pgStore != nil {
		a.pgStore.Close()
	}
}
