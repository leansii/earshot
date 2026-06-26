-- SPDX-License-Identifier: Apache-2.0
-- Named queries for sqlc codegen. These mirror the hand-written queries in
-- pgxstore.go; once generated, pgxstore can call db.New(pool) accessors.

-- name: AppKeyByHash :one
SELECT id, project_id, name, key_hash, prefix, rate_limit_per_min, created_at, revoked_at
FROM app_keys
WHERE key_hash = $1 AND revoked_at IS NULL;

-- name: GetProject :one
SELECT id, slug, name, github_owner, github_repo, github_token, labels,
       llm_model, embed_model, tracker, feedback_autofile, task_strategy,
       connector_config, created_at
FROM projects
WHERE id = $1;

-- name: CreateFeedback :one
INSERT INTO feedback (id, project_id, app_id, version, user_ref, route, severity, body, context)
VALUES (gen_random_uuid()::text, $1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, created_at;

-- name: GetFeedback :one
SELECT id, project_id, app_id, version, user_ref, route, severity, body, context, created_at
FROM feedback
WHERE id = $1;

-- name: ItemsForDedup :many
SELECT id, project_id, category, title, summary, scrubbed_body, status, embedding,
       feedback_count, github_issue_number, github_issue_url, created_at, updated_at
FROM items
WHERE project_id = $1 AND array_length(embedding, 1) > 0
ORDER BY id;

-- name: CreateItem :one
INSERT INTO items (id, project_id, category, title, summary, scrubbed_body, status, embedding, feedback_count)
VALUES (gen_random_uuid()::text, $1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, created_at, updated_at;

-- name: LinkFeedback :exec
INSERT INTO feedback_items (feedback_id, item_id) VALUES ($1, $2)
ON CONFLICT (feedback_id) DO UPDATE SET item_id = EXCLUDED.item_id;

-- name: BumpItemCount :exec
UPDATE items SET feedback_count = feedback_count + 1, updated_at = now() WHERE id = $1;

-- name: GetItem :one
SELECT id, project_id, category, title, summary, scrubbed_body, status, embedding,
       feedback_count, github_issue_number, github_issue_url, created_at, updated_at
FROM items
WHERE id = $1;

-- name: ListItems :many
SELECT id, project_id, category, title, summary, scrubbed_body, status, embedding,
       feedback_count, github_issue_number, github_issue_url, created_at, updated_at
FROM items
WHERE project_id = $1 AND status = $2
ORDER BY id;

-- name: UpdateItemStatus :execrows
UPDATE items SET status = $2, updated_at = now() WHERE id = $1;

-- name: MarkItemFiled :execrows
UPDATE items SET status = 'filed', github_issue_number = $2, github_issue_url = $3, updated_at = now()
WHERE id = $1;

-- name: CreateTask :one
INSERT INTO tasks (id, project_id, item_id, title, description, status)
VALUES (gen_random_uuid()::text, $1, $2, $3, $4, $5)
RETURNING id, created_at, updated_at;

-- name: GetTask :one
SELECT id, project_id, item_id, title, description, status, tracker_ref, tracker_number, tracker_url, created_at, updated_at
FROM tasks WHERE id = $1;

-- name: TasksForItem :many
SELECT id, project_id, item_id, title, description, status, tracker_ref, tracker_number, tracker_url, created_at, updated_at
FROM tasks WHERE item_id = $1 ORDER BY id;

-- name: ListTasks :many
SELECT id, project_id, item_id, title, description, status, tracker_ref, tracker_number, tracker_url, created_at, updated_at
FROM tasks WHERE project_id = $1 AND status = $2 ORDER BY id;

-- name: UpdateTaskStatus :execrows
UPDATE tasks SET status = $2, updated_at = now() WHERE id = $1;

-- name: MarkTaskPushed :execrows
UPDATE tasks SET status = 'filed', tracker_ref = $2, tracker_number = $3, tracker_url = $4, updated_at = now()
WHERE id = $1;
