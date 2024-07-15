// Package fsstore is the filesystem + SQLite implementation of
// store.Store (ADR-0002). Source bytes live as files under
// <root>/objects/<hash[:2]>/<hash>/, metadata is indexed in a SQLite
// database at <root>/metadata.db. The filesystem is the truth; SQLite
// is a rebuildable index.
package fsstore

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const (
	objectsDir    = "objects"
	metadataDB    = "metadata.db"
	versionFile   = "version.txt"
	schemaVersion = "1"
)

// Store is a filesystem + SQLite backing for store.Store. Safe for
// concurrent use within the process per the ADR-0002 single-writer
// contract.
type Store struct {
	root  string
	db    *sql.DB
	clock func() time.Time
}

// Option configures a Store at construction.
type Option func(*Store)

// WithClock injects a clock for deterministic created_at timestamps in
// tests. Default is time.Now.
func WithClock(c func() time.Time) Option {
	return func(s *Store) { s.clock = c }
}

// New opens (or creates) a Store rooted at the given directory. Idempotent:
// a second call against an existing root reuses the schema and the object
// tree. Returns the open Store; the caller closes it.
func New(root string, opts ...Option) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("fsstore: root is required")
	}
	if err := os.MkdirAll(filepath.Join(root, objectsDir), 0o755); err != nil {
		return nil, fmt.Errorf("fsstore: create objects dir: %w", err)
	}
	if err := writeVersionFile(root); err != nil {
		return nil, err
	}

	dsn := dsnFor(filepath.Join(root, metadataDB))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("fsstore: open db: %w", err)
	}
	// Single-writer per ADR-0002. WAL allows concurrent readers because
	// read connections do not contend with the writer. Note: with a cap
	// of 1, any code that holds a *sql.Tx and then calls a method on
	// the *sql.DB directly will deadlock — operations under a Tx must
	// go through the Tx, never the DB.
	db.SetMaxOpenConns(1)
	if err := applySchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{root: root, db: db, clock: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Close releases the SQLite handle. The on-disk object tree and
// metadata.db remain.
func (s *Store) Close() error {
	return s.db.Close()
}

// Root returns the filesystem root the Store was opened against.
// Exposed for diagnostics and tests; not part of the store.Store contract.
func (s *Store) Root() string { return s.root }

// dsnFor wires the SQLite DSN with _pragma parameters so the driver
// applies WAL, synchronous=FULL, busy_timeout=5s, and foreign_keys=ON
// on every connection the pool opens — not just the first one borrowed
// after sql.Open.
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
		return fmt.Errorf("fsstore: apply schema: %w", err)
	}
	return nil
}

func writeVersionFile(root string) error {
	path := filepath.Join(root, versionFile)
	switch _, err := os.Stat(path); {
	case err == nil:
		return nil
	case !os.IsNotExist(err):
		return fmt.Errorf("fsstore: stat version file: %w", err)
	}
	if err := os.WriteFile(path, []byte(schemaVersion+"\n"), 0o644); err != nil {
		return fmt.Errorf("fsstore: write version file: %w", err)
	}
	return nil
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS artifacts (
    hash               TEXT PRIMARY KEY,
    content_type       TEXT NOT NULL,
    state              TEXT NOT NULL,
    created_at         INTEGER NOT NULL,
    created_by         TEXT NOT NULL,
    source_commit_sha  TEXT,
    description        TEXT,
    derived_by_version TEXT,
    has_snapshot       INTEGER NOT NULL,
    has_diagnose       INTEGER NOT NULL,
    deprecated_at      INTEGER,
    deprecated_reason  TEXT
);

CREATE INDEX IF NOT EXISTS idx_artifacts_state_created
    ON artifacts(state, created_at DESC);

-- Append-only log: each reassignment of a tag inserts a new row. The
-- current head for a tag is the row with the largest assigned_at.
CREATE TABLE IF NOT EXISTS tags (
    tag         TEXT NOT NULL,
    hash        TEXT NOT NULL,
    assigned_at INTEGER NOT NULL,
    PRIMARY KEY (tag, assigned_at),
    FOREIGN KEY (hash) REFERENCES artifacts(hash)
);

CREATE INDEX IF NOT EXISTS idx_tags_current
    ON tags(tag, assigned_at DESC);
`
