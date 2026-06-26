-- SPDX-License-Identifier: Apache-2.0
-- +goose Up
-- +goose StatementBegin

-- Connector configuration moves onto the project. The feedback-issue now
-- auto-files (gate moved to the task level); auto_file is replaced by the more
-- specific feedback_autofile plus tracker/strategy settings.
ALTER TABLE projects DROP COLUMN IF EXISTS auto_file;
ALTER TABLE projects ADD COLUMN tracker           TEXT    NOT NULL DEFAULT 'github';
ALTER TABLE projects ADD COLUMN feedback_autofile BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE projects ADD COLUMN task_strategy     TEXT    NOT NULL DEFAULT 'sub_issues';
ALTER TABLE projects ADD COLUMN connector_config  JSONB   NOT NULL DEFAULT '{}'::jsonb;

-- Tasks are the actionable, human-gated items the triage-agent derives from a
-- feedback-issue. They always live here first (source of truth) and are pushed
-- to the tracker only after approval.
CREATE TABLE tasks (
    id             TEXT PRIMARY KEY,
    project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    item_id        TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    title          TEXT NOT NULL DEFAULT '',
    description    TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'needs_triage',
    tracker_ref    TEXT NOT NULL DEFAULT '',
    tracker_number INTEGER NOT NULL DEFAULT 0,
    tracker_url    TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_tasks_item ON tasks(item_id);
CREATE INDEX idx_tasks_project_status ON tasks(project_id, status);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS tasks;
ALTER TABLE projects DROP COLUMN IF EXISTS connector_config;
ALTER TABLE projects DROP COLUMN IF EXISTS task_strategy;
ALTER TABLE projects DROP COLUMN IF EXISTS feedback_autofile;
ALTER TABLE projects DROP COLUMN IF EXISTS tracker;
ALTER TABLE projects ADD COLUMN auto_file BOOLEAN NOT NULL DEFAULT FALSE;
-- +goose StatementEnd
