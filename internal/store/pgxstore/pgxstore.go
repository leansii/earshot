// SPDX-License-Identifier: Apache-2.0

// Package pgxstore is the Postgres implementation of store.Store, built on pgx.
//
// Queries are hand-written here for the v1 skeleton. The repository ships an
// sqlc.yaml and a queries/ directory so these can be migrated to generated,
// type-safe accessors without changing the store.Store interface — that
// migration is tracked as a follow-up.
package pgxstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leansii/earshot/internal/core"
	"github.com/leansii/earshot/internal/store"
)

// Store is a Postgres-backed store.Store.
type Store struct {
	pool *pgxpool.Pool
}

var _ store.Store = (*Store)(nil)

// New connects a pool to the given database URL.
func New(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("pgxstore: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("pgxstore: ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Pool exposes the underlying pool (used to construct the River queue against
// the same database).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// Tx wraps a pgx.Tx so it satisfies core.Tx while still exposing the
// underlying transaction to the River queue adapter for transactional enqueue.
type Tx struct{ tx pgx.Tx }

// Commit implements core.Tx.
func (t *Tx) Commit(ctx context.Context) error { return t.tx.Commit(ctx) }

// Rollback implements core.Tx.
func (t *Tx) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }

// PgxTx exposes the underlying pgx.Tx. The River queue adapter type-asserts to
// this interface to enqueue in the same transaction.
func (t *Tx) PgxTx() pgx.Tx { return t.tx }

// BeginTx implements store.Store.
func (s *Store) BeginTx(ctx context.Context) (core.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("pgxstore: begin: %w", err)
	}
	return &Tx{tx: tx}, nil
}

func txOf(tx core.Tx) (pgx.Tx, error) {
	t, ok := tx.(*Tx)
	if !ok {
		return nil, fmt.Errorf("pgxstore: foreign tx type %T", tx)
	}
	return t.tx, nil
}

// --- Auth ---

// AppKeyByHash implements store.Store.
func (s *Store) AppKeyByHash(ctx context.Context, keyHash string) (core.AppKey, error) {
	const q = `SELECT id, project_id, name, key_hash, prefix, rate_limit_per_min, created_at, revoked_at
	           FROM app_keys WHERE key_hash = $1 AND revoked_at IS NULL`
	var k core.AppKey
	err := s.pool.QueryRow(ctx, q, keyHash).Scan(
		&k.ID, &k.ProjectID, &k.Name, &k.KeyHash, &k.Prefix, &k.RateLimitPerMin, &k.CreatedAt, &k.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.AppKey{}, store.ErrNotFound
	}
	if err != nil {
		return core.AppKey{}, err
	}
	return k, nil
}

// Project implements store.Store.
func (s *Store) Project(ctx context.Context, id string) (core.Project, error) {
	const q = `SELECT id, slug, name, github_owner, github_repo, github_token, labels,
	                  llm_model, embed_model, auto_file, created_at
	           FROM projects WHERE id = $1`
	var p core.Project
	err := s.pool.QueryRow(ctx, q, id).Scan(
		&p.ID, &p.Slug, &p.Name, &p.GitHubOwner, &p.GitHubRepo, &p.GitHubToken, &p.Labels,
		&p.LLMModel, &p.EmbedModel, &p.AutoFile, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.Project{}, store.ErrNotFound
	}
	if err != nil {
		return core.Project{}, err
	}
	return p, nil
}

// --- Ingest ---

// CreateFeedback implements store.Store, inserting within tx.
func (s *Store) CreateFeedback(ctx context.Context, tx core.Tx, fb core.Feedback) (core.Feedback, error) {
	pgtx, err := txOf(tx)
	if err != nil {
		return core.Feedback{}, err
	}
	ctxJSON, err := json.Marshal(orEmptyMap(fb.Context))
	if err != nil {
		return core.Feedback{}, err
	}
	const q = `INSERT INTO feedback (id, project_id, app_id, version, user_ref, route, severity, body, context)
	           VALUES (gen_random_uuid()::text, $1, $2, $3, $4, $5, $6, $7, $8)
	           RETURNING id, created_at`
	err = pgtx.QueryRow(ctx, q, fb.ProjectID, fb.AppID, fb.Version, fb.UserRef, fb.Route, fb.Severity, fb.Body, ctxJSON).
		Scan(&fb.ID, &fb.CreatedAt)
	if err != nil {
		return core.Feedback{}, fmt.Errorf("pgxstore: insert feedback: %w", err)
	}
	return fb, nil
}

// --- Pipeline ---

// Feedback implements store.Store.
func (s *Store) Feedback(ctx context.Context, id string) (core.Feedback, error) {
	const q = `SELECT id, project_id, app_id, version, user_ref, route, severity, body, context, created_at
	           FROM feedback WHERE id = $1`
	var fb core.Feedback
	var ctxJSON []byte
	err := s.pool.QueryRow(ctx, q, id).Scan(
		&fb.ID, &fb.ProjectID, &fb.AppID, &fb.Version, &fb.UserRef, &fb.Route, &fb.Severity, &fb.Body, &ctxJSON, &fb.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.Feedback{}, store.ErrNotFound
	}
	if err != nil {
		return core.Feedback{}, err
	}
	_ = json.Unmarshal(ctxJSON, &fb.Context)
	return fb, nil
}

// ItemsForDedup implements store.Store.
func (s *Store) ItemsForDedup(ctx context.Context, projectID string) ([]core.Item, error) {
	const q = `SELECT id, project_id, category, title, summary, scrubbed_body, status, embedding,
	                  feedback_count, github_issue_number, github_issue_url, created_at, updated_at
	           FROM items WHERE project_id = $1 AND array_length(embedding, 1) > 0
	           ORDER BY id`
	rows, err := s.pool.Query(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanItems(rows)
}

// CreateItem implements store.Store.
func (s *Store) CreateItem(ctx context.Context, item core.Item) (core.Item, error) {
	if item.Status == "" {
		item.Status = core.StatusNeedsTriage
	}
	const q = `INSERT INTO items (id, project_id, category, title, summary, scrubbed_body, status, embedding, feedback_count)
	           VALUES (gen_random_uuid()::text, $1, $2, $3, $4, $5, $6, $7, $8)
	           RETURNING id, created_at, updated_at`
	err := s.pool.QueryRow(ctx, q,
		item.ProjectID, item.Category, item.Title, item.Summary, item.ScrubbedBody, item.Status,
		float32sToFloat64s(item.Embedding), item.FeedbackCount).
		Scan(&item.ID, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return core.Item{}, fmt.Errorf("pgxstore: insert item: %w", err)
	}
	return item, nil
}

// LinkFeedback implements store.Store.
func (s *Store) LinkFeedback(ctx context.Context, feedbackID, itemID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx,
		`INSERT INTO feedback_items (feedback_id, item_id) VALUES ($1, $2)
		 ON CONFLICT (feedback_id) DO UPDATE SET item_id = EXCLUDED.item_id`, feedbackID, itemID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE items SET feedback_count = feedback_count + 1, updated_at = now() WHERE id = $1`, itemID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// --- Triage ---

// Item implements store.Store.
func (s *Store) Item(ctx context.Context, id string) (core.Item, error) {
	const q = `SELECT id, project_id, category, title, summary, scrubbed_body, status, embedding,
	                  feedback_count, github_issue_number, github_issue_url, created_at, updated_at
	           FROM items WHERE id = $1`
	rows, err := s.pool.Query(ctx, q, id)
	if err != nil {
		return core.Item{}, err
	}
	defer rows.Close()
	items, err := scanItems(rows)
	if err != nil {
		return core.Item{}, err
	}
	if len(items) == 0 {
		return core.Item{}, store.ErrNotFound
	}
	return items[0], nil
}

// ListItems implements store.Store.
func (s *Store) ListItems(ctx context.Context, projectID string, status core.Status) ([]core.Item, error) {
	q := `SELECT id, project_id, category, title, summary, scrubbed_body, status, embedding,
	             feedback_count, github_issue_number, github_issue_url, created_at, updated_at
	      FROM items WHERE project_id = $1`
	args := []any{projectID}
	if status != "" {
		q += ` AND status = $2`
		args = append(args, string(status))
	}
	q += ` ORDER BY id`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanItems(rows)
}

// UpdateItemStatus implements store.Store.
func (s *Store) UpdateItemStatus(ctx context.Context, id string, status core.Status) error {
	ct, err := s.pool.Exec(ctx, `UPDATE items SET status = $2, updated_at = now() WHERE id = $1`, id, string(status))
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// MarkItemFiled implements store.Store.
func (s *Store) MarkItemFiled(ctx context.Context, id string, issueNumber int, issueURL string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE items SET status = $2, github_issue_number = $3, github_issue_url = $4, updated_at = now() WHERE id = $1`,
		id, string(core.StatusFiled), issueNumber, issueURL)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// --- Admin ---

// CreateProject implements store.Store.
func (s *Store) CreateProject(ctx context.Context, p core.Project) (core.Project, error) {
	if p.ID == "" {
		p.ID = ""
	}
	const q = `INSERT INTO projects (id, slug, name, github_owner, github_repo, github_token, labels, llm_model, embed_model, auto_file)
	           VALUES (COALESCE(NULLIF($1,''), gen_random_uuid()::text), $2, $3, $4, $5, $6, $7, $8, $9, $10)
	           RETURNING id, created_at`
	if p.LLMModel == "" {
		p.LLMModel = "fake:classifier-v1"
	}
	if p.EmbedModel == "" {
		p.EmbedModel = "fake:hash-256"
	}
	err := s.pool.QueryRow(ctx, q,
		p.ID, p.Slug, p.Name, p.GitHubOwner, p.GitHubRepo, p.GitHubToken, p.Labels, p.LLMModel, p.EmbedModel, p.AutoFile).
		Scan(&p.ID, &p.CreatedAt)
	if err != nil {
		return core.Project{}, fmt.Errorf("pgxstore: insert project: %w", err)
	}
	return p, nil
}

// CreateAppKey implements store.Store.
func (s *Store) CreateAppKey(ctx context.Context, k core.AppKey) (core.AppKey, error) {
	if k.RateLimitPerMin == 0 {
		k.RateLimitPerMin = 60
	}
	const q = `INSERT INTO app_keys (id, project_id, name, key_hash, prefix, rate_limit_per_min)
	           VALUES (COALESCE(NULLIF($1,''), gen_random_uuid()::text), $2, $3, $4, $5, $6)
	           RETURNING id, created_at`
	err := s.pool.QueryRow(ctx, q, k.ID, k.ProjectID, k.Name, k.KeyHash, k.Prefix, k.RateLimitPerMin).
		Scan(&k.ID, &k.CreatedAt)
	if err != nil {
		return core.AppKey{}, fmt.Errorf("pgxstore: insert app key: %w", err)
	}
	return k, nil
}

// --- helpers ---

func scanItems(rows pgx.Rows) ([]core.Item, error) {
	var out []core.Item
	for rows.Next() {
		var it core.Item
		var emb []float64
		if err := rows.Scan(
			&it.ID, &it.ProjectID, &it.Category, &it.Title, &it.Summary, &it.ScrubbedBody, &it.Status, &emb,
			&it.FeedbackCount, &it.GitHubIssueNumber, &it.GitHubIssueURL, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		it.Embedding = float64sToFloat32s(emb)
		out = append(out, it)
	}
	return out, rows.Err()
}

func orEmptyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func float32sToFloat64s(in []float32) []float64 {
	out := make([]float64, len(in))
	for i, v := range in {
		out[i] = float64(v)
	}
	return out
}

func float64sToFloat32s(in []float64) []float32 {
	out := make([]float32, len(in))
	for i, v := range in {
		out[i] = float32(v)
	}
	return out
}
