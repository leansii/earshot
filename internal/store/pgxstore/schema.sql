-- SPDX-License-Identifier: Apache-2.0
-- Flattened schema for sqlc codegen (no goose Up/Down directives, no DROPs).
-- Keep in sync with migrations/00001_init.sql.

CREATE TABLE projects (
    id            TEXT PRIMARY KEY,
    slug          TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL DEFAULT '',
    github_owner  TEXT NOT NULL DEFAULT '',
    github_repo   TEXT NOT NULL DEFAULT '',
    github_token  TEXT NOT NULL DEFAULT '',
    labels        TEXT[] NOT NULL DEFAULT '{}',
    llm_model     TEXT NOT NULL DEFAULT 'fake:classifier-v1',
    embed_model   TEXT NOT NULL DEFAULT 'fake:hash-256',
    auto_file     BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE app_keys (
    id                 TEXT PRIMARY KEY,
    project_id         TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name               TEXT NOT NULL DEFAULT '',
    key_hash           TEXT NOT NULL UNIQUE,
    prefix             TEXT NOT NULL DEFAULT '',
    rate_limit_per_min INTEGER NOT NULL DEFAULT 60,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at         TIMESTAMPTZ
);

CREATE TABLE feedback (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    app_id      TEXT NOT NULL DEFAULT '',
    version     TEXT NOT NULL DEFAULT '',
    user_ref    TEXT NOT NULL DEFAULT '',
    route       TEXT NOT NULL DEFAULT '',
    severity    TEXT NOT NULL DEFAULT '',
    body        TEXT NOT NULL,
    context     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE items (
    id                  TEXT PRIMARY KEY,
    project_id          TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    category            TEXT NOT NULL DEFAULT '',
    title               TEXT NOT NULL DEFAULT '',
    summary             TEXT NOT NULL DEFAULT '',
    scrubbed_body       TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'needs_triage',
    embedding           DOUBLE PRECISION[] NOT NULL DEFAULT '{}',
    feedback_count      INTEGER NOT NULL DEFAULT 0,
    github_issue_number INTEGER NOT NULL DEFAULT 0,
    github_issue_url    TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE feedback_items (
    feedback_id TEXT PRIMARY KEY REFERENCES feedback(id) ON DELETE CASCADE,
    item_id     TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE
);
