<!-- SPDX-License-Identifier: Apache-2.0 -->
# Earshot

**Earshot** turns raw user feedback into a tracked **feedback-issue** plus a set
of actionable **tasks**. Apps (web, Telegram bots, CLIs, …) `POST` feedback to a
small, fast ingest API. Earshot enriches and classifies it, de-duplicates it,
scrubs secrets/PII, auto-files the **feedback-issue** to a tracker (GitHub today;
connectors are pluggable), and a **triage-agent** decomposes it into tasks.
Earshot's database is the source of truth; tasks live there first and are pushed
to the tracker only after a **human approves** each one. Multi-project,
multi-tenant.

> Status: **v1 skeleton** — an end-to-end, hermetically-tested vertical slice.
> See [Out of scope](#whats-intentionally-out-of-scope-v1) for what is stubbed.

---

## Architecture

```
  App (web / Telegram / CLI)
        │  POST /v1/feedback   (X-API-Key: esk_…)
        ▼
  ┌──────────────┐   one DB transaction    ┌─────────────────────┐
  │  Ingest API  │ ──────────────────────► │ feedback row + job  │  transactional
  │   (chi)      │   persist + enqueue      │  (outbox)           │  outbox
  └──────────────┘                          └──────────┬──────────┘
        │ 202 Accepted                                  │
        │  (no LLM / tracker on this path)              ▼
        │                                  ┌──────────────────────────┐
        │                                  │          Worker          │
        │                                  │  enrich → dedup → scrub   │
        │                                  │  → auto-file feedback-issue──┐ (scrub = hard barrier)
        │                                  │  → triage-agent: tasks    │  │
        │                                  └────────────┬─────────────┘  ▼
        │                                               │          ┌───────────┐
        │                                               │          │ Connector │ per-project
        │                                               ▼          │ (GitHub…) │ fine-grained PAT
        │                                  tasks in DB: needs_triage└─────┬─────┘
        │                                               │                 ▼
        │   ═══════════  HUMAN TASK GATE  ══════════════╪═══════     feedback-issue
        │                                               │ approve task
        ▼                                               ▼  (POST /v1/tasks/{id}/approve | earshot approve)
  GET /v1/triage  (feedback-issues)         push per project TaskStrategy:
  GET /v1/tasks   (tasks)                    sub-issue · checklist · comment
```

Each boundary is an interface, so backends swap without touching callers:

| Concern   | Interface           | v1 implementation        | Swappable to            |
|-----------|---------------------|--------------------------|-------------------------|
| Queue     | `queue.Queue`       | River (Postgres) / memory| Redis (asynq), SQS, …   |
| Store     | `store.Store`       | Postgres (pgx) / memory  | any SQL store           |
| LLM agent | `llm.Provider`      | `anthropic:…` / `deepseek:…` / `gemini:…` / `fake:…` | any `provider:model` |
| Embeddings| `embed.Embedder`    | `fake:…`                 | Voyage, OpenAI, local   |
| Tracker   | `connector.Tracker` | GitHub / mock            | Jira, Linear, GHES, …   |

---

## Security posture

These seven principles are baked in from the first commit; they double as the
threat model. See [SECURITY.md](SECURITY.md) for reporting and detail.

1. **Ingest is fast & dumb.** `POST /v1/feedback` only authenticates, validates,
   and enqueues an `enrich` job **in the same DB transaction** as the feedback
   insert (transactional outbox). No LLM or GitHub calls happen on the request
   path — a committed feedback row always has a matching job, and a rolled-back
   one never leaks one.
2. **Async work lives behind an interface.** River (Postgres-backed) is the
   production queue; an in-memory queue backs dev/tests. Jobs are small,
   reference rows by ID, and are retryable/idempotent. Swapping the backend is
   implementing `queue.Queue`, not a rewrite.
3. **Human gate at the task level — tasks never auto-push.** The triage-agent
   only ever produces tasks in `needs_triage`, stored in Earshot's DB (the
   source of truth). The *only* code path that pushes a task to the tracker is an
   explicit human `approve`. The feedback-issue itself may auto-file (per-project
   `feedback_autofile`, default on) — but only **after** scrubbing.
4. **Feedback is an injection vector.** Bodies are treated strictly as data — for
   both classification and the triage-agent: fixed system instructions, the input
   fenced in a delimiter, **no tools** given to the model, **no secrets** in its
   context. The agent sees only already-scrubbed content.
5. **Least privilege to the tracker.** Each project carries its **own fine-grained
   PAT**, scoped to a single repo with *Issues: read & write*, passed to the
   connector per call — never a shared, org-wide classic token. Stored via secret
   references / `*_FILE` indirection.
6. **PII / privacy — scrub is a hard barrier.** An explicit `scrub` stage redacts
   emails, API tokens, and private keys **before** the feedback-issue is filed or
   any task is pushed; pushes re-scrub as defense in depth.
7. **Per-app auth + rate limiting.** Every app has its own API key, hashed at
   rest, rate-limited per key. A key maps to exactly one project — tenant
   isolation means a key can never reach another project's data.

---

## Quick start

### Run locally (no database, in-memory everything)

```bash
go run ./cmd/earshot serve        # EARSHOT_QUEUE defaults to "memory"
# → ingest API + in-process worker on :8080, fake LLM/embedder
```

In memory mode, drafts live only in that process; use the Postgres path below to
persist and to drive the CLI/approve flow across processes.

### Run with Postgres + River (production shape)

```bash
docker compose up -d            # Postgres (pgvector) on :5432
export EARSHOT_DATABASE_URL=postgres://earshot:earshot@localhost:5432/earshot?sslmode=disable
export EARSHOT_QUEUE=river

go run ./cmd/earshot migrate                       # app schema + River tables
go run ./cmd/earshot seed-project --slug demo \
    --owner leansii --repo earshot \
    --token-file ./project.pat --labels feedback \
    --tracker github --task-strategy sub_issues --feedback-autofile
# → prints an API key ONCE — store it securely

go run ./cmd/earshot serve      # API on :8080
go run ./cmd/earshot worker     # separate worker process
```

---

## How an app integrates

Send feedback with the project's API key. The structured context helps triage;
the `body` is free text.

```bash
curl -sS -X POST http://localhost:8080/v1/feedback \
  -H "X-API-Key: esk_xxxxxxxx…" \
  -H "Content-Type: application/json" \
  -d '{
        "app_id":   "mobile-ios",
        "version":  "3.4.1",
        "user_ref": "u_8123",
        "route":    "/checkout",
        "severity": "high",
        "body":     "Tapping Pay does nothing and the app freezes."
      }'
# → 202 Accepted  {"id":"…","status":"accepted"}
```

`severity` (if set) must be `low|medium|high|critical`. The endpoint returns
immediately; enrichment happens asynchronously.

---

## Per-project configuration

A project decides how Earshot projects its data to a tracker. Set via
`seed-project` flags (or env defaults); the database is always the source of
truth, so a tracker outage or `none` never loses data.

| Setting             | Values                              | Default       | Meaning |
|---------------------|-------------------------------------|---------------|---------|
| `tracker`           | `github` · `none`                   | `github`      | which connector receives issues (more via `connector.Tracker`) |
| `feedback_autofile` | `true` · `false`                    | `true`        | auto-create the feedback-issue after scrubbing |
| `task_strategy`     | `sub_issues` · `checklist` · `comments` | `sub_issues` | how approved tasks attach to the feedback-issue |

Provider-specific settings (e.g. a Jira project key) go in a per-project
`connector_config` JSON column, so new connectors need no schema change.

## The feedback → tasks → approve flow (the human gate)

1. The worker enriches and de-duplicates feedback, scrubs it, and **auto-files
   the feedback-issue** to the tracker (if `feedback_autofile`). The
   triage-agent then drafts **tasks** in `needs_triage`. **No task is pushed.**
2. A human reviews the feedback-issues and their tasks:
   ```bash
   curl -H "X-API-Key: esk_…" "http://localhost:8080/v1/triage"                  # feedback-issues
   curl -H "X-API-Key: esk_…" "http://localhost:8080/v1/triage/<item-id>/tasks"  # its tasks
   curl -H "X-API-Key: esk_…" "http://localhost:8080/v1/tasks?status=needs_triage"
   # or:  earshot triage-list --project <id>   /   earshot tasks --project <id>
   ```
3. A human explicitly approves a **task** — the **only** path that pushes it:
   ```bash
   curl -X POST -H "X-API-Key: esk_…" http://localhost:8080/v1/tasks/<task-id>/approve
   # or:  earshot approve --id <task-id>
   ```
   Earshot attaches the task to its feedback-issue per the project's
   `task_strategy`, using that project's fine-grained token. Rejecting
   (`/reject`) never touches the tracker.

The test suite asserts that no task reaches the (mocked) tracker until an
explicit approval, and that nothing is filed un-scrubbed — see
`internal/triage/pipeline_test.go`.

---

## The queue-migration path

`queue.Queue` is the seam. v1 ships two implementations:

- `internal/memory` — in-memory, single-process (dev/tests).
- `internal/queue/riverq` — River over Postgres; `InsertTx` enqueues in the
  caller's transaction for exact outbox semantics.

To move to Redis/asynq, SQS, etc., implement `queue.Queue` (and a consumer) in a
new package and select it in `internal/app`. Callers — ingest, worker, pipeline
— do not change.

---

## What's intentionally out of scope (v1)

Filed as issues, not built: auto-file mode + confidence thresholds; web/Telegram
triage UI; per-platform SDKs; multi-language; advanced PII models; alternate
queue backends (Redis/SQS); analytics/dashboards; Vouch integration. The
Postgres store uses hand-written pgx queries with an `sqlc.yaml` ready for
codegen; embeddings ship only a deterministic fake provider.

---

## Development

```bash
make build   # go build ./...
make test    # go test ./...   (offline, fakes/mocks — no network)
make lint    # go vet + golangci-lint
make check   # all of the above
```

See [CONTRIBUTING.md](CONTRIBUTING.md). Licensed under [Apache-2.0](LICENSE).
