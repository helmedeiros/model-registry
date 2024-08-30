package fsstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/helmedeiros/model-registry/internal/store"
)

// List implements store.Reader. Ordering is (created_at DESC, hash ASC)
// per ADR-0002; cursor is exclusive and names the last hash returned on
// the previous page.
func (s *Store) List(ctx context.Context, opts store.ListOptions) (store.Page, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = store.DefaultListLimit
	}
	if limit > store.MaxListLimit {
		limit = store.MaxListLimit
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return store.Page{}, fmt.Errorf("fsstore: begin list tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	cursorAt, cursorKnown, err := resolveListCursor(ctx, tx, opts.Cursor)
	if err != nil {
		return store.Page{}, err
	}

	q := `SELECT hash, content_type, state, created_at, created_by,
	             source_commit_sha, description, derived_by_version, rules_json
	      FROM artifacts WHERE 1=1`
	args := make([]any, 0, 4)
	if opts.State != "" {
		q += ` AND state = ?`
		args = append(args, string(opts.State))
	}
	if cursorKnown {
		// Strict less-than on the anchor's created_at, then ascending-hash
		// tiebreak inside the equal-timestamp bucket. Matches the memstore
		// reference and the conformance bar (testListTieBreaksAscending).
		// The OR arm cannot use idx_artifacts_state_created (hash is not
		// in the index), so the equal-timestamp bucket is scanned in
		// place. The < 50 ms bar was measured against a fixture with
		// monotonically distinct timestamps; clustered-timestamp bulk
		// imports would re-open this bar.
		q += ` AND (created_at < ? OR (created_at = ? AND hash > ?))`
		args = append(args, cursorAt, cursorAt, opts.Cursor)
	}
	q += ` ORDER BY created_at DESC, hash ASC LIMIT ?`
	// Fetch one extra row so the presence of a next page is observable
	// without a second COUNT query.
	args = append(args, limit+1)

	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return store.Page{}, fmt.Errorf("fsstore: list query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]store.Summary, 0, limit+1)
	for rows.Next() {
		var (
			hash, ct, state, createdBy           string
			createdAtMS                          int64
			commitSHA, description, derivedBy    sql.NullString
			rulesJSON                            sql.NullString
		)
		if err := rows.Scan(&hash, &ct, &state, &createdAtMS, &createdBy,
			&commitSHA, &description, &derivedBy, &rulesJSON); err != nil {
			return store.Page{}, fmt.Errorf("fsstore: list scan: %w", err)
		}
		rules, err := decodeRules(rulesJSON)
		if err != nil {
			return store.Page{}, err
		}
		items = append(items, store.Summary{
			Hash:        store.Hash(hash),
			ContentType: store.ContentType(ct),
			State:       store.State(state),
			Metadata: store.Metadata{
				CreatedAt:        time.UnixMilli(createdAtMS).UTC(),
				CreatedBy:        createdBy,
				SourceCommitSHA:  commitSHA.String,
				Description:      description.String,
				DerivedByVersion: derivedBy.String,
				Rules:            rules,
			},
		})
	}
	if err := rows.Err(); err != nil {
		return store.Page{}, fmt.Errorf("fsstore: list rows: %w", err)
	}

	page := store.Page{}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = string(items[limit-1].Hash)
	} else {
		page.Items = items
	}
	return page, nil
}

func resolveListCursor(ctx context.Context, tx *sql.Tx, cursor string) (int64, bool, error) {
	if cursor == "" {
		return 0, false, nil
	}
	var createdAtMS int64
	err := tx.QueryRowContext(ctx,
		`SELECT created_at FROM artifacts WHERE hash = ?`, cursor).Scan(&createdAtMS)
	if isNoRows(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("fsstore: resolve cursor: %w", err)
	}
	return createdAtMS, true, nil
}
