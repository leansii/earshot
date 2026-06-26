-- SPDX-License-Identifier: Apache-2.0
-- +goose Up
-- +goose StatementBegin

-- pgvector is enabled for production ANN dedup. The v1 skeleton stores
-- embeddings as a portable float8[] and computes cosine in-app; switching the
-- `embedding` column to vector(N) with an ivfflat/hnsw index is a follow-up.
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE projects (
    id            TEXT PRIMARY KEY,
    slug          TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL DEFAULT '',
    github_owner  TEXT NOT NULL DEFAULT '',
    github_repo   TEXT NOT NULL DEFAULT '',
    -- Per-project fine-grained PAT (single repo, Issues: read & write).
    -- In production this is a secret reference, not stored plaintext.
    github_token  TEXT NOT NULL DEFAULT '',
    labels        TEXT[] NOT NULL DEFAULT '{}',
    llm_model     TEXT NOT NULL DEFAULT 'fake:classifier-v1',
    embed_model   TEXT NOT NULL DEFAULT 'fake:hash-256',
    -- Auto-file is OFF by default and unused by the v1 pipeline.
    auto_file     BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE app_keys (
    id                TEXT PRIMARY KEY,
    project_id        TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name              TEXT NOT NULL DEFAULT '',
    key_hash          TEXT NOT NULL UNIQUE,
    prefix            TEXT NOT NULL DEFAULT '',
    rate_limit_per_min INTEGER NOT NULL DEFAULT 60,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at        TIMESTAMPTZ
);
CREATE INDEX idx_app_keys_project ON app_keys(project_id);

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
CREATE INDEX idx_feedback_project ON feedback(project_id);

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
CREATE INDEX idx_items_project_status ON items(project_id, status);

CREATE TABLE feedback_items (
    feedback_id TEXT PRIMARY KEY REFERENCES feedback(id) ON DELETE CASCADE,
    item_id     TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE
);
CREATE INDEX idx_feedback_items_item ON feedback_items(item_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS feedback_items;
DROP TABLE IF EXISTS items;
DROP TABLE IF EXISTS feedback;
DROP TABLE IF EXISTS app_keys;
DROP TABLE IF EXISTS projects;
-- +goose StatementEnd
