<!-- SPDX-License-Identifier: Apache-2.0 -->
# Contributing to Earshot

Thanks for your interest! Earshot is open-source under [Apache-2.0](LICENSE).
By contributing you agree your work is licensed under the same terms.

## Prerequisites

- **Go 1.23+**
- Optional (production paths and codegen):
  - Docker / Docker Compose (Postgres + pgvector)
  - [`golangci-lint`](https://golangci-lint.run) v2
  - [`goose`](https://github.com/pressly/goose) (migrations CLI)
  - [`sqlc`](https://sqlc.dev) (when wiring generated queries)

The default build and the entire test suite are **hermetic** — no network, no
database, no real LLM or GitHub — using in-memory and fake implementations.

## Build, test, lint

```bash
make build        # go build ./...
make test         # go test ./...        (offline)
make lint         # go vet ./... + golangci-lint run
make check        # build + vet + lint + test
make fmt          # gofmt -w
```

CI runs `make check`; please ensure it is green before opening a PR.

## Project layout

```
cmd/earshot/          single binary: serve | worker | migrate | seed-project | approve | …
internal/
  core/               shared domain types (no internal deps)
  ingest/             HTTP front door: auth, rate limit, validation, triage routes
  queue/              Queue interface + River driver (riverq/)
  store/              Store interface + Postgres driver (pgxstore/)
  memory/             in-memory Store+Queue for dev/tests
  llm/                Classifier interface + anthropic + fake
  embed/              Embedder interface + fake
  dedup/              cosine near-duplicate detection
  scrub/              secret/PII redaction
  triage/             enrich→dedup→scrub→triage pipeline + human-gated approve
  ghissue/            GitHub issue adapter + mock
  config/  app/       env config + dependency wiring
migrations/           goose SQL migrations
```

## Conventions

- **Formatting:** `gofmt` (run `make fmt`). No unformatted files in PRs.
- **Linting:** keep `golangci-lint run` clean (config in `.golangci.yml`).
- **SPDX headers:** every Go file starts with
  `// SPDX-License-Identifier: Apache-2.0`.
- **Interfaces first:** new backends implement an existing interface; don't leak
  a concrete dependency (pgx, River, go-github) into callers.
- **Security invariants:** never add a code path that files a GitHub issue
  outside an explicit human approval; never place secrets in the LLM context;
  always scope queries by `ProjectID`.
- **Tests:** add hermetic tests with fakes/mocks. Anything touching a real DB
  must be opt-in (e.g. build tag / testcontainers) and skipped by default.

## Commits & pull requests

- Use clear, imperative commit subjects (e.g. `ingest: enforce per-key limit`).
- Keep PRs focused; describe the change, the rationale, and how you tested it.
- Reference issues with `Fixes #NN` where applicable.
- Do not include AI co-authorship trailers.

## Database changes

- Add a new numbered migration under `migrations/` (goose format). Never edit a
  released migration.
- The Postgres store currently uses hand-written pgx queries; an `sqlc.yaml` and
  `internal/store/pgxstore/queries/` are provided for moving to generated,
  type-safe accessors. Run `sqlc generate` and wire the output behind the
  existing `store.Store` interface.

## Reporting security issues

Please follow [SECURITY.md](SECURITY.md) — do not open public issues for
vulnerabilities.
