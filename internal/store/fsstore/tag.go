package fsstore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/helmedeiros/model-registry/internal/store"
)

func (s *Store) Tag(ctx context.Context, tag string, h store.Hash) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("fsstore: begin tag tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var state string
	err = tx.QueryRowContext(ctx, `SELECT state FROM artifacts WHERE hash = ?`, string(h)).Scan(&state)
	if isNoRows(err) {
		return store.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("fsstore: read state: %w", err)
	}
	if store.State(state) == store.StateDeprecated {
		return store.ErrInvalidTransition
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tags (tag, hash, assigned_at) VALUES (?, ?, ?)`,
		tag, string(h), s.clock().UnixMilli(),
	); err != nil {
		return fmt.Errorf("fsstore: insert tag: %w", err)
	}

	if store.State(state) == store.StateStaged {
		if _, err := tx.ExecContext(ctx,
			`UPDATE artifacts SET state = ? WHERE hash = ?`,
			string(store.StateActive), string(h),
		); err != nil {
			return fmt.Errorf("fsstore: activate artifact: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("fsstore: commit tag tx: %w", err)
	}
	return nil
}

func (s *Store) ResolveTag(ctx context.Context, tag string) (store.Hash, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", fmt.Errorf("fsstore: begin resolve tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var h string
	err = tx.QueryRowContext(ctx,
		`SELECT hash FROM tags WHERE tag = ? ORDER BY assigned_at DESC LIMIT 1`,
		tag).Scan(&h)
	if isNoRows(err) {
		return "", store.ErrTagUnknown
	}
	if err != nil {
		return "", fmt.Errorf("fsstore: resolve tag: %w", err)
	}
	return store.Hash(h), nil
}

func (s *Store) ListTags(ctx context.Context) (map[string]store.Hash, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT tag, hash FROM (
		   SELECT tag, hash,
		          ROW_NUMBER() OVER (PARTITION BY tag ORDER BY assigned_at DESC) AS rn
		   FROM tags
		 ) WHERE rn = 1`)
	if err != nil {
		return nil, fmt.Errorf("fsstore: list tags query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]store.Hash{}
	for rows.Next() {
		var (
			tag, hash string
		)
		if err := rows.Scan(&tag, &hash); err != nil {
			return nil, fmt.Errorf("fsstore: list tags scan: %w", err)
		}
		out[tag] = store.Hash(hash)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("fsstore: list tags iter: %w", err)
	}
	return out, nil
}

