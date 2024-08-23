// Package fsaudit is the SQLite-backed audit.Store (durable sibling
// of memaudit). Both must satisfy audittest.RunConformance.
package fsaudit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/store"
)

// Store is a SQLite-backed audit.Store.
type Store struct {
	db *sql.DB
}

// New opens or creates a Store at the given path.
func New(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("fsaudit: path is required")
	}
	db, err := sql.Open("sqlite", dsnFor(path))
	if err != nil {
		return nil, fmt.Errorf("fsaudit: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := applySchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := addTraceIDColumn(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close releases the SQLite handle.
func (s *Store) Close() error { return s.db.Close() }

func dsnFor(path string) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(ON)")
	return path + "?" + q.Encode()
}

func applySchema(db *sql.DB) error {
	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("fsaudit: apply schema: %w", err)
	}
	return nil
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS audit_entry (
    id            TEXT PRIMARY KEY,
    operator      TEXT NOT NULL,
    action        TEXT NOT NULL,
    target        TEXT NOT NULL,
    artifact_hash TEXT,
    reason        TEXT,
    at            INTEGER NOT NULL,
    trace_id      TEXT
);

CREATE INDEX IF NOT EXISTS idx_audit_entry_recent ON audit_entry(at DESC, id DESC);

-- forward-compatible ALTER for an existing file written before the
-- trace_id column was introduced. SQLite ignores the duplicate-column
-- error when the column already exists, so we tolerate it.
`

// addTraceIDColumn is run after applySchema to bring older audit.db
// files up to the current schema. Idempotent: if trace_id already
// exists the ALTER errors with "duplicate column name", which we
// swallow.
func addTraceIDColumn(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE audit_entry ADD COLUMN trace_id TEXT`)
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "duplicate column name") {
		return nil
	}
	return fmt.Errorf("fsaudit: add trace_id column: %w", err)
}

// List implements audit.Reader.
func (s *Store) List(ctx context.Context, opts audit.ListOptions) (audit.Page, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = audit.DefaultListLimit
	}
	if limit > audit.MaxListLimit {
		limit = audit.MaxListLimit
	}

	const orderClause = ` ORDER BY at DESC, id DESC LIMIT ?`
	var (
		rows *sql.Rows
		err  error
	)
	// First-page or unknown-cursor → restart from head. Only decode
	// when there's something to decode so the hot first-page path
	// allocates no error value.
	useCursor := false
	var cAt int64
	var cID string
	if opts.Cursor != "" {
		if a, h, derr := decodeCursor(opts.Cursor); derr == nil {
			useCursor, cAt, cID = true, a, h
		}
	}
	if !useCursor {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, operator, action, target, artifact_hash, reason, at, trace_id
			   FROM audit_entry`+orderClause,
			limit+1)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, operator, action, target, artifact_hash, reason, at, trace_id
			   FROM audit_entry
			  WHERE (at < ? OR (at = ? AND id < ?))`+orderClause,
			cAt, cAt, cID, limit+1)
	}
	if err != nil {
		return audit.Page{}, fmt.Errorf("fsaudit: list query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]audit.Entry, 0, limit+1)
	for rows.Next() {
		var (
			id, operator, action, target sql.NullString
			artifactHash, reason         sql.NullString
			traceID                      sql.NullString
			at                           int64
		)
		if err := rows.Scan(&id, &operator, &action, &target, &artifactHash, &reason, &at, &traceID); err != nil {
			return audit.Page{}, fmt.Errorf("fsaudit: list scan: %w", err)
		}
		items = append(items, audit.Entry{
			ID:           id.String,
			Operator:     operator.String,
			Action:       action.String,
			Target:       target.String,
			ArtifactHash: store.Hash(artifactHash.String),
			Reason:       reason.String,
			At:           time.UnixMilli(at).UTC(),
			TraceID:      traceID.String,
		})
	}
	if err := rows.Err(); err != nil {
		return audit.Page{}, fmt.Errorf("fsaudit: list rows: %w", err)
	}

	page := audit.Page{}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = cursorOf(items[limit-1])
	} else {
		page.Items = items
	}
	return page, nil
}

func cursorOf(e audit.Entry) string {
	return e.At.UTC().Format(time.RFC3339Nano) + "|" + e.ID
}

func decodeCursor(c string) (int64, string, error) {
	pipe := strings.LastIndex(c, "|")
	if pipe < 0 {
		return 0, "", fmt.Errorf("fsaudit: malformed cursor %q", c)
	}
	t, err := time.Parse(time.RFC3339Nano, c[:pipe])
	if err != nil {
		return 0, "", fmt.Errorf("fsaudit: malformed cursor %q: %w", c, err)
	}
	return t.UnixMilli(), c[pipe+1:], nil
}

// Record implements audit.Writer.
func (s *Store) Record(ctx context.Context, entry audit.Entry) error {
	switch {
	case entry.ID == "":
		return audit.ErrIDRequired
	case entry.Operator == "":
		return audit.ErrOperatorRequired
	case entry.Action == "":
		return audit.ErrActionRequired
	case entry.Target == "":
		return audit.ErrTargetRequired
	case entry.At.IsZero():
		return audit.ErrAtRequired
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_entry(id, operator, action, target, artifact_hash, reason, at, trace_id)
		      VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID, entry.Operator, entry.Action, entry.Target,
		nullableString(string(entry.ArtifactHash)), nullableString(entry.Reason),
		entry.At.UnixMilli(),
		nullableString(entry.TraceID),
	)
	if isUniqueConstraintErr(err) {
		return audit.ErrDuplicateID
	}
	if err != nil {
		return fmt.Errorf("fsaudit: record entry: %w", err)
	}
	return nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// isUniqueConstraintErr recognises the PK collision raised by the
// audit_entry.id PRIMARY KEY. Typed match via *sqlite.Error.Code so a
// future CHECK constraint with a UNIQUE-shaped message cannot
// false-positive into ErrDuplicateID.
func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	var serr *sqlite.Error
	if !errors.As(err, &serr) {
		return false
	}
	c := serr.Code()
	return c == sqlitelib.SQLITE_CONSTRAINT_PRIMARYKEY ||
		c == sqlitelib.SQLITE_CONSTRAINT_UNIQUE
}
