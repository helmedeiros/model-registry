package fsstore

import (
	"context"
	"fmt"

	"github.com/helmedeiros/model-registry/internal/store"
)

func (s *Store) Deprecate(ctx context.Context, h store.Hash, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("fsstore: begin deprecate tx: %w", err)
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
		`UPDATE artifacts
		    SET state = ?, deprecated_at = ?, deprecated_reason = ?
		  WHERE hash = ?`,
		string(store.StateDeprecated), s.clock().UnixMilli(), reason, string(h),
	); err != nil {
		return fmt.Errorf("fsstore: update deprecation: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("fsstore: commit deprecate tx: %w", err)
	}
	return nil
}
