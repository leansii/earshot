<!-- SPDX-License-Identifier: Apache-2.0 -->
# Security Policy

Earshot ingests untrusted user feedback and holds credentials that can write to
GitHub repositories. We take its security posture seriously and welcome reports.

## Reporting a vulnerability

**Please do not open a public issue for security problems.**

- Preferred: use **GitHub Private Vulnerability Reporting** for this repository
  (Security ▸ *Report a vulnerability*). This opens a private advisory thread.
- Alternatively, email **security@leansii.dev** with the details below. If you
  need encryption, request our PGP key in your first message.

Please include:

- A description of the issue and its impact.
- Steps to reproduce (a minimal proof-of-concept is ideal).
- Affected version / commit and configuration.

### Our commitment

- We acknowledge reports within **3 business days**.
- We aim to provide an initial assessment within **10 business days**.
- We will keep you updated on remediation and coordinate disclosure timing with
  you. We credit reporters who wish to be named.

Please act in good faith: give us reasonable time to remediate before public
disclosure, and avoid privacy violations, data destruction, or service
degradation while testing.

## Supported versions

Earshot is pre-1.0. Security fixes are applied to the `main` branch; until a
tagged release line exists, please test against the latest `main`.

## Security model (summary)

Earshot is built around seven principles (the full version, with rationale, is
in the README "Security posture" section):

1. **Ingest is fast & dumb** — authenticate, validate, and enqueue in one DB
   transaction; no LLM/GitHub calls on the request path.
2. **Async work behind an interface** — retryable, idempotent jobs.
3. **Human gate at the task level** — the triage-agent never pushes tasks; only
   an explicit human approval pushes a task to the tracker. The feedback-issue
   may auto-file (`feedback_autofile`, default on) but only after scrubbing.
4. **Feedback is an injection vector** — bodies are data, never instructions, for
   both the classifier and the triage-agent; the model gets no tools and no
   secrets, and the agent sees only scrubbed content.
5. **Least privilege** — per-project fine-grained PAT scoped to one repo with
   *Issues: read & write*; never a shared org-wide token.
6. **PII / privacy** — scrub is a hard barrier: nothing is filed or pushed with
   un-scrubbed content.
7. **Per-app auth + rate limiting** — hashed API keys, per-key limits, strict
   one-key-to-one-project tenant isolation.

### Handling of secrets

- API keys are stored only as SHA-256 hashes; the raw key is shown once.
- GitHub tokens are loaded via `*_FILE` / secret references and are never
  logged, never serialized in API responses (`json:"-"`), and passed to the
  GitHub adapter per call.
- `.gitignore` excludes `.env`, `*.env`, `*.pem`, `*.key`, token files, and
  build artifacts. Do not commit real secrets; CI and reviewers treat any
  committed credential as an incident.

### Known limitations (v1)

The scrubber targets high-confidence secret/PII patterns and is a first line of
defense, not a guarantee. Job-level idempotency for re-driven enrich jobs and a
production embedding provider are tracked as follow-ups. Review approved drafts
before filing to public repositories.
