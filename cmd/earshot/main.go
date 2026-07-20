// SPDX-License-Identifier: Apache-2.0

// Command earshot is the single binary for the Earshot feedback service. It
// dispatches to subcommands: serve, worker, migrate, and a few admin helpers.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/leansii/earshot/internal/apikey"
	"github.com/leansii/earshot/internal/app"
	"github.com/leansii/earshot/internal/config"
	"github.com/leansii/earshot/internal/core"
	"github.com/leansii/earshot/internal/ingest"
)

// version is overridable at build time via -ldflags.
var version = "dev"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe(ctx, log)
	case "worker":
		err = cmdWorker(ctx, log, os.Args[2:])
	case "migrate":
		err = cmdMigrate(ctx, log, os.Args[2:])
	case "seed-project":
		err = cmdSeedProject(ctx, log, os.Args[2:])
	case "triage-list":
		err = cmdTriageList(ctx, log, os.Args[2:])
	case "tasks":
		err = cmdTasks(ctx, log, os.Args[2:])
	case "approve":
		err = cmdApprove(ctx, log, os.Args[2:])
	case "reject":
		err = cmdReject(ctx, log, os.Args[2:])
	case "version", "-v", "--version":
		fmt.Println("earshot", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}

	if err != nil {
		log.Error("command failed", "cmd", os.Args[1], "err", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `earshot — feedback ingest & triage service

Usage:
  earshot serve          Run the HTTP ingest+triage API (and worker, in memory mode)
  earshot worker         Run a job worker (River backend; --drain empties the queue and exits)
  earshot migrate        Apply database migrations (app schema + River)
  earshot seed-project   Create a project and print a new app API key
  earshot triage-list    List feedback-issues for a project
  earshot tasks          List agent-generated tasks for a project
  earshot approve        Approve a task and push it to the tracker
  earshot reject         Reject a task
  earshot version        Print version

Configuration is read from the environment (see .env.example).
`)
}

func loadApp(ctx context.Context, log *slog.Logger) (*app.App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return app.Build(ctx, cfg, log)
}

func cmdServe(ctx context.Context, log *slog.Logger) error {
	a, err := loadApp(ctx, log)
	if err != nil {
		return err
	}
	defer a.Close()

	svc := ingest.NewService(a.Store, a.Queue, a.Approver, log)
	srv := &http.Server{
		Addr:              a.Cfg.HTTPAddr,
		Handler:           svc.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// In memory mode the queue lives in this process, so run the worker here.
	if a.InMemory() {
		go func() {
			if err := a.RunWorker(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("worker stopped", "err", err)
			}
		}()
		log.Info("running in-process worker (memory queue)")
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Info("ingest API listening", "addr", a.Cfg.HTTPAddr, "queue", a.Cfg.Queue)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func cmdWorker(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	drain := fs.Bool("drain", false, "process runnable jobs until the queue is empty, then exit")
	_ = fs.Parse(args)

	a, err := loadApp(ctx, log)
	if err != nil {
		return err
	}
	defer a.Close()
	if a.InMemory() {
		return errors.New("worker: memory queue is in-process only; use `serve` or set EARSHOT_QUEUE=river")
	}

	if *drain {
		log.Info("worker draining queue", "queue", a.Cfg.Queue)
		if err := a.DrainWorker(ctx); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		log.Info("queue drained")
		return nil
	}

	log.Info("worker started", "queue", a.Cfg.Queue)
	if err := a.RunWorker(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func cmdMigrate(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	dir := fs.String("dir", "migrations", "directory of goose .sql migrations")
	dbURL := fs.String("db", os.Getenv("EARSHOT_DATABASE_URL"), "database URL")
	_ = fs.Parse(args)
	if *dbURL == "" {
		return errors.New("migrate: set --db or EARSHOT_DATABASE_URL")
	}

	// 1. Application schema via goose.
	db, err := sql.Open("pgx", *dbURL)
	if err != nil {
		return fmt.Errorf("migrate: open: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, db, *dir); err != nil {
		return fmt.Errorf("migrate: goose up: %w", err)
	}
	log.Info("app schema migrated")

	// 2. River's own tables.
	pool, err := pgxpool.New(ctx, *dbURL)
	if err != nil {
		return fmt.Errorf("migrate: pool: %w", err)
	}
	defer pool.Close()
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("migrate: river migrator: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("migrate: river up: %w", err)
	}
	log.Info("River schema migrated")
	return nil
}

func cmdSeedProject(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("seed-project", flag.ExitOnError)
	slug := fs.String("slug", "", "project slug (required)")
	name := fs.String("name", "", "project display name")
	owner := fs.String("owner", "", "GitHub owner/org")
	repo := fs.String("repo", "", "GitHub repository")
	tokenFile := fs.String("token-file", "", "path to the project's fine-grained PAT")
	labels := fs.String("labels", "feedback", "comma-separated issue labels")
	rpm := fs.Int("rate-limit", 60, "per-key rate limit (requests/min)")
	tracker := fs.String("tracker", "github", "issue tracker: github|none")
	strategy := fs.String("task-strategy", "sub_issues", "how tasks attach: sub_issues|checklist|comments")
	autofile := fs.Bool("feedback-autofile", true, "auto-create the feedback-issue after scrubbing")
	_ = fs.Parse(args)
	if *slug == "" {
		return errors.New("seed-project: --slug is required")
	}
	if ts := core.TaskStrategy(*strategy); !ts.Valid() {
		return fmt.Errorf("seed-project: invalid --task-strategy %q", *strategy)
	}

	a, err := loadApp(ctx, log)
	if err != nil {
		return err
	}
	defer a.Close()

	token := ""
	if *tokenFile != "" {
		b, err := os.ReadFile(*tokenFile)
		if err != nil {
			return fmt.Errorf("seed-project: read token file: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}

	proj, err := a.Store.CreateProject(ctx, core.Project{
		Slug: *slug, Name: *name, GitHubOwner: *owner, GitHubRepo: *repo,
		GitHubToken: token, Labels: splitCSV(*labels),
		Tracker: core.TrackerProvider(*tracker), TaskStrategy: core.TaskStrategy(*strategy),
		FeedbackAutoFile: *autofile,
	})
	if err != nil {
		return err
	}

	rawKey, prefix, err := apikey.Generate()
	if err != nil {
		return err
	}
	if _, err := a.Store.CreateAppKey(ctx, core.AppKey{
		ProjectID: proj.ID, Name: "default", KeyHash: apikey.Hash(rawKey),
		Prefix: prefix, RateLimitPerMin: *rpm,
	}); err != nil {
		return err
	}

	fmt.Printf("Project created: %s (%s)\n", proj.Slug, proj.ID)
	fmt.Printf("API key (shown once, store it securely):\n  %s\n", rawKey)
	return nil
}

func cmdTriageList(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("triage-list", flag.ExitOnError)
	projectID := fs.String("project", "", "project ID (required)")
	status := fs.String("status", string(core.StatusNeedsTriage), "status filter")
	_ = fs.Parse(args)
	if *projectID == "" {
		return errors.New("triage-list: --project is required")
	}
	a, err := loadApp(ctx, log)
	if err != nil {
		return err
	}
	defer a.Close()
	items, err := a.Store.ListItems(ctx, *projectID, core.Status(*status))
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Println("(no items)")
		return nil
	}
	for _, it := range items {
		issue := ""
		if it.GitHubIssueNumber > 0 {
			issue = fmt.Sprintf(" issue#%d", it.GitHubIssueNumber)
		}
		fmt.Printf("%s  [%s]%s  %s  (%s, %d report(s))\n", it.ID, it.Status, issue, it.Title, it.Category, it.FeedbackCount)
	}
	return nil
}

func cmdTasks(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("tasks", flag.ExitOnError)
	projectID := fs.String("project", "", "project ID (required)")
	status := fs.String("status", string(core.StatusNeedsTriage), "status filter (empty for all)")
	_ = fs.Parse(args)
	if *projectID == "" {
		return errors.New("tasks: --project is required")
	}
	a, err := loadApp(ctx, log)
	if err != nil {
		return err
	}
	defer a.Close()
	tasks, err := a.Store.ListTasks(ctx, *projectID, core.Status(*status))
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		fmt.Println("(no tasks)")
		return nil
	}
	for _, t := range tasks {
		ref := ""
		if t.TrackerRef != "" {
			ref = "  -> " + t.TrackerRef
		}
		fmt.Printf("%s  [%s]  %s  (item %s)%s\n", t.ID, t.Status, t.Title, t.ItemID, ref)
	}
	return nil
}

func cmdApprove(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("approve", flag.ExitOnError)
	id := fs.String("id", "", "task ID (required)")
	_ = fs.Parse(args)
	if *id == "" {
		return errors.New("approve: --id is required")
	}
	a, err := loadApp(ctx, log)
	if err != nil {
		return err
	}
	defer a.Close()
	pushed, err := a.Approver.Approve(ctx, *id)
	if err != nil {
		return err
	}
	if pushed.TrackerURL != "" {
		fmt.Printf("Task %s approved and pushed (%s): %s\n", pushed.ID, pushed.TrackerRef, pushed.TrackerURL)
	} else {
		fmt.Printf("Task %s approved (tracker=none, not pushed)\n", pushed.ID)
	}
	return nil
}

func cmdReject(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("reject", flag.ExitOnError)
	id := fs.String("id", "", "task ID (required)")
	_ = fs.Parse(args)
	if *id == "" {
		return errors.New("reject: --id is required")
	}
	a, err := loadApp(ctx, log)
	if err != nil {
		return err
	}
	defer a.Close()
	if err := a.Approver.Reject(ctx, *id); err != nil {
		return err
	}
	fmt.Printf("Rejected %s\n", *id)
	return nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
