<!-- SPDX-License-Identifier: Apache-2.0 -->
# Earshot

**Earshot** turns raw user feedback into well-triaged GitHub issues — without ever
auto-filing. Apps (web, Telegram bots, CLIs, …) `POST` feedback to a small, fast
ingest API. Earshot enriches and classifies it with an LLM, de-duplicates it,
scrubs secrets/PII, and produces a **draft** that a human must approve before a
single byte reaches GitHub. It is multi-project and multi-tenant.

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
        │  (no LLM / GitHub on this path)               ▼
        │                                      ┌──────────────────┐
        │                                      │      Worker      │
        │                                      │  enrich (LLM)    │
        │                                      │  → dedup (cosine)│
        │                                      │  → scrub (PII)   │
        │                                      │  → triage        │
        │                                      └────────┬─────────┘
        │                                               ▼
        │                                   draft item: status = needs_triage
        │                                               │
        │  ════════════════  HUMAN TRIAGE GATE  ════════╪════════════
        │                                               │ approve  (POST /v1/triage/{id}/approve
        │                                               │           or `earshot approve --id …`)
        ▼                                               ▼
  GET /v1/triage                              ┌──────────────────┐
  (list drafts)                               │  GitHub adapter  │  per-project
                                              │  (go-github)     │  fine-grained PAT
                                              └────────┬─────────┘
                                                       ▼
                                                  GitHub Issue
```

Each boundary is an interface, so backends swap without touching callers:

| Concern   | Interface           | v1 implementation        | Swappable to            |
|-----------|---------------------|--------------------------|-------------------------|
| Queue     | `queue.Queue`       | River (Postgres) / memory| Redis (asynq), SQS, …   |
| Store     | `store.Store`       | Postgres (pgx) / memory  | any SQL store           |
| LLM       | `llm.Classifier`    | `anthropic:…` / `fake:…` | any `provider:model`    |
| Embeddings| `embed.Embedder`    | `fake:…`                 | Voyage, OpenAI, local   |
| GitHub    | `ghissue.Issuer`    | go-github / mock         | GitHub App, GHES        |

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
3. **Human triage gate — never auto-file by default.** The pipeline only ever
   produces a draft in `needs_triage`. The *only* code path to GitHub is an
   explicit human `approve`. (A per-project `auto_file` flag exists in the schema
   but is **OFF** and unused by v1.)
4. **Feedback is an injection vector.** Bodies are treated strictly as data:
   fixed system instructions, the body fenced in a delimiter, **no tools** given
   to the model, and **no secrets** in its context.
5. **Least privilege to GitHub.** Each project carries its **own fine-grained
   PAT**, scoped to a single repo with *Issues: read & write*, passed to the
   adapter per call — never a shared, org-wide classic token. Stored via secret
   references / `*_FILE` indirection.
6. **PII / privacy.** An explicit `scrub` stage redacts emails, API tokens, and
   private keys **before** anything can become a (possibly public) issue;
   approval re-scrubs as defense in depth.
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
    --token-file ./project.pat --labels feedback
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

## The triage → approve flow (the human gate)

1. The worker enriches the feedback into a **draft** with status `needs_triage`.
   **No GitHub issue is created.**
2. A human lists and reviews drafts:
   ```bash
   curl -H "X-API-Key: esk_…" "http://localhost:8080/v1/triage?status=needs_triage"
   # or:  earshot triage-list --project <project-id>
   ```
3. A human explicitly approves — the **only** path that files an issue:
   ```bash
   curl -X POST -H "X-API-Key: esk_…" http://localhost:8080/v1/triage/<item-id>/approve
   # or:  earshot approve --id <item-id>
   ```
   Earshot then files a **scrubbed** issue to the project's repo using that
   project's fine-grained token. Rejecting (`/reject`) never calls GitHub.

The test suite asserts that nothing reaches the (mocked) GitHub client until an
explicit approval — see `internal/triage/pipeline_test.go`.

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
