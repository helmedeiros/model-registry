// Package fsstate is the SQLite-backed envstate.Store. Schema lives
// in a single file (<store-root>/envstate.db) opened with the same
// DSN pragma posture as fsstore: WAL + synchronous=FULL +
// busy_timeout + foreign_keys ON applied on every pool connection.
// Multi-statement Writer methods run under one write Tx so a process
// kill mid-promote either commits both the env_state UPDATE and the
// env_history INSERT or neither.
package fsstate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/helmedeiros/model-registry/internal/envstate"
	"github.com/helmedeiros/model-registry/internal/store"
)

// Store is a SQLite-backed envstate.Store.
type Store struct {
	db    *sql.DB
	clock func() time.Time
}

// Option configures a Store at construction.
type Option func(*Store)

// WithClock injects a clock for deterministic timestamps in tests.
func WithClock(c func() time.Time) Option {
	return func(s *Store) { s.clock = c }
}

// New opens or creates a Store at the given path. Idempotent; a second
// call against the same path reuses the file.
func New(path string, opts ...Option) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("fsstate: path is required")
	}
	db, err := sql.Open("sqlite", dsnFor(path))
	if err != nil {
		return nil, fmt.Errorf("fsstate: open db: %w", err)
	}
	db.SetMaxOpenConns(1) // single-writer posture, same as fsstore.
	if err := applySchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{db: db, clock: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
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
		return fmt.Errorf("fsstate: apply schema: %w", err)
	}
	return nil
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS env_state (
    env                 TEXT PRIMARY KEY,
    champion_hash       TEXT,
    champion_by         TEXT,
    champion_at         INTEGER,
    challenger_hash     TEXT,
    challenger_by       TEXT,
    challenger_at       INTEGER,
    updated_at          INTEGER
);

CREATE TABLE IF NOT EXISTS env_history (
    env       TEXT NOT NULL,
    kind      TEXT NOT NULL,
    from_hash TEXT,
    to_hash   TEXT,
    operator  TEXT,
    reason    TEXT,
    at        INTEGER NOT NULL,
    PRIMARY KEY (env, at)
);

CREATE INDEX IF NOT EXISTS idx_env_history_recent ON env_history(env, at DESC);
`

// Get implements envstate.Reader.
func (s *Store) Get(ctx context.Context, env string) (envstate.State, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT champion_hash, champion_by, champion_at,
		        challenger_hash, challenger_by, challenger_at,
		        updated_at
		   FROM env_state WHERE env = ?`, env)
	var (
		championHash, championBy, challengerHash, challengerBy sql.NullString
		championAt, challengerAt, updatedAt                    sql.NullInt64
	)
	switch err := row.Scan(&championHash, &championBy, &championAt,
		&challengerHash, &challengerBy, &challengerAt, &updatedAt); {
	case errors.Is(err, sql.ErrNoRows):
		return envstate.State{Env: env}, nil
	case err != nil:
		return envstate.State{}, fmt.Errorf("fsstate: get env_state: %w", err)
	}

	out := envstate.State{Env: env}
	if championHash.Valid {
		out.Champion = &envstate.Role{
			Hash:       store.Hash(championHash.String),
			PromotedBy: championBy.String,
			PromotedAt: time.UnixMilli(championAt.Int64).UTC(),
		}
	}
	if challengerHash.Valid {
		out.Challenger = &envstate.Role{
			Hash:       store.Hash(challengerHash.String),
			PromotedBy: challengerBy.String,
			PromotedAt: time.UnixMilli(challengerAt.Int64).UTC(),
		}
	}
	if updatedAt.Valid {
		out.UpdatedAt = time.UnixMilli(updatedAt.Int64).UTC()
	}
	return out, nil
}

// History implements envstate.Reader. Cursor format is the same opaque
// `RFC3339Nano|to_hash` string memstate emits; SQL keyset pagination
// pushes ORDER BY + LIMIT into SQLite so an env with N transitions
// fetches O(limit) rows, not O(N).
func (s *Store) History(ctx context.Context, env string, opts envstate.ListOptions) (envstate.HistoryPage, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = envstate.DefaultListLimit
	}
	if limit > envstate.MaxListLimit {
		limit = envstate.MaxListLimit
	}

	// Fetch limit+1 to know whether a next page exists without a
	// second COUNT round-trip.
	const orderClause = ` ORDER BY at DESC, to_hash ASC LIMIT ?`
	var (
		rows *sql.Rows
		err  error
	)
	// Unknown / malformed cursors restart from the top — matches memstate
	// which does the same via "cursor not found in walk -> start = 0".
	cAt, cHash, derr := decodeCursor(opts.Cursor)
	if opts.Cursor == "" || derr != nil {
		rows, err = s.db.QueryContext(ctx,
			`SELECT kind, from_hash, to_hash, operator, reason, at
			   FROM env_history WHERE env = ?`+orderClause,
			env, limit+1)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT kind, from_hash, to_hash, operator, reason, at
			   FROM env_history
			  WHERE env = ?
			    AND (at < ? OR (at = ? AND to_hash > ?))`+orderClause,
			env, cAt, cAt, cHash, limit+1)
	}
	if err != nil {
		return envstate.HistoryPage{}, fmt.Errorf("fsstate: history query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]envstate.Transition, 0, limit+1)
	for rows.Next() {
		var (
			kind, fromHash, toHash, operator, reason sql.NullString
			at                                       int64
		)
		if err := rows.Scan(&kind, &fromHash, &toHash, &operator, &reason, &at); err != nil {
			return envstate.HistoryPage{}, fmt.Errorf("fsstate: history scan: %w", err)
		}
		items = append(items, envstate.Transition{
			Env:      env,
			Kind:     envstate.Kind(kind.String),
			FromHash: store.Hash(fromHash.String),
			ToHash:   store.Hash(toHash.String),
			Operator: operator.String,
			Reason:   reason.String,
			At:       time.UnixMilli(at).UTC(),
		})
	}
	if err := rows.Err(); err != nil {
		return envstate.HistoryPage{}, fmt.Errorf("fsstate: history rows: %w", err)
	}

	page := envstate.HistoryPage{}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = cursorOf(items[limit-1])
	} else {
		page.Items = items
	}
	return page, nil
}

func cursorOf(t envstate.Transition) string {
	return t.At.UTC().Format(time.RFC3339Nano) + "|" + string(t.ToHash)
}

func decodeCursor(c string) (int64, string, error) {
	pipe := strings.LastIndex(c, "|")
	if pipe < 0 {
		return 0, "", fmt.Errorf("fsstate: malformed cursor %q", c)
	}
	t, err := time.Parse(time.RFC3339Nano, c[:pipe])
	if err != nil {
		return 0, "", fmt.Errorf("fsstate: malformed cursor %q: %w", c, err)
	}
	return t.UnixMilli(), c[pipe+1:], nil
}

// PreviousChampion implements envstate.Reader. Runs the lookup under a
// read-only tx so the current-champion read and the prior-champion
// scan see one snapshot — a concurrent PromoteChampion between the two
// reads cannot make the predicate filter on the wrong "current" hash.
func (s *Store) PreviousChampion(ctx context.Context, env string) (store.Hash, error) {
	if env == "" {
		return "", envstate.ErrEnvRequired
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", fmt.Errorf("fsstate: begin previous-champion tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	currentHash, err := readChampionHash(ctx, tx, env)
	if err != nil {
		return "", err
	}
	return readPreviousChampionHash(ctx, tx, env, currentHash)
}

// queryRowCtx is the minimal contract shared by *sql.DB and *sql.Tx
// that the champion-lookup helpers need.
type queryRowCtx interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readChampionHash(ctx context.Context, q queryRowCtx, env string) (string, error) {
	var h sql.NullString
	err := q.QueryRowContext(ctx,
		`SELECT champion_hash FROM env_state WHERE env = ?`, env).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) || !h.Valid {
		return "", envstate.ErrNoChampion
	}
	if err != nil {
		return "", fmt.Errorf("fsstate: read champion hash: %w", err)
	}
	return h.String, nil
}

func readPreviousChampionHash(ctx context.Context, q queryRowCtx, env, excludeHash string) (store.Hash, error) {
	var prior sql.NullString
	err := q.QueryRowContext(ctx,
		`SELECT to_hash FROM env_history
		   WHERE env = ? AND kind = ? AND to_hash != ?
		  ORDER BY at DESC
		  LIMIT 1`,
		env, string(envstate.KindChampionPromoted), excludeHash).Scan(&prior)
	if errors.Is(err, sql.ErrNoRows) || !prior.Valid {
		return "", envstate.ErrNoPreviousChampion
	}
	if err != nil {
		return "", fmt.Errorf("fsstate: read previous champion hash: %w", err)
	}
	return store.Hash(prior.String), nil
}

// PromoteChampion implements envstate.Writer. UPDATE env_state +
// INSERT env_history under one write Tx; the clock read happens
// inside BeginTx's transaction context so concurrent goroutines
// produce a monotonic At sequence regardless of schedule.
func (s *Store) PromoteChampion(ctx context.Context, env string, h store.Hash, operator, reason string) (store.Hash, error) {
	if env == "" {
		return "", envstate.ErrEnvRequired
	}
	if h == "" {
		return "", envstate.ErrHashRequired
	}
	if operator == "" {
		return "", envstate.ErrOperatorRequired
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("fsstate: begin promote tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	nowMS, err := monotonicAtMS(ctx, tx, env, s.clock())
	if err != nil {
		return "", err
	}

	var previousHash sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT champion_hash FROM env_state WHERE env = ?`, env).Scan(&previousHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("fsstate: read prior champion: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO env_state(env, champion_hash, champion_by, champion_at, updated_at)
		      VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(env) DO UPDATE SET
		     champion_hash = excluded.champion_hash,
		     champion_by   = excluded.champion_by,
		     champion_at   = excluded.champion_at,
		     updated_at    = excluded.updated_at`,
		env, string(h), operator, nowMS, nowMS,
	); err != nil {
		return "", fmt.Errorf("fsstate: write env_state: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO env_history(env, kind, from_hash, to_hash, operator, reason, at)
		      VALUES (?, ?, ?, ?, ?, ?, ?)`,
		env, string(envstate.KindChampionPromoted),
		nullableString(previousHash.String), string(h),
		operator, reason, nowMS,
	); err != nil {
		return "", fmt.Errorf("fsstate: write env_history: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("fsstate: commit promote tx: %w", err)
	}
	return store.Hash(previousHash.String), nil
}

// RollbackChampion implements envstate.Writer.
func (s *Store) RollbackChampion(ctx context.Context, env, operator, reason string) (store.Hash, error) {
	if env == "" {
		return "", envstate.ErrEnvRequired
	}
	if operator == "" {
		return "", envstate.ErrOperatorRequired
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("fsstate: begin rollback tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	currentHash, err := readChampionHash(ctx, tx, env)
	if err != nil {
		return "", err
	}

	prior, err := readPreviousChampionHash(ctx, tx, env, currentHash)
	if err != nil {
		return "", err
	}

	nowMS, err := monotonicAtMS(ctx, tx, env, s.clock())
	if err != nil {
		return "", err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE env_state
		    SET champion_hash = ?, champion_by = ?, champion_at = ?, updated_at = ?
		  WHERE env = ?`,
		string(prior), operator, nowMS, nowMS, env,
	); err != nil {
		return "", fmt.Errorf("fsstate: update champion: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO env_history(env, kind, from_hash, to_hash, operator, reason, at)
		      VALUES (?, ?, ?, ?, ?, ?, ?)`,
		env, string(envstate.KindChampionRolledBack),
		currentHash, string(prior),
		operator, reason, nowMS,
	); err != nil {
		return "", fmt.Errorf("fsstate: write rollback history: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("fsstate: commit rollback tx: %w", err)
	}
	return prior, nil
}

// PromoteChallenger implements envstate.Writer (ADR-0009).
func (s *Store) PromoteChallenger(ctx context.Context, env string, h store.Hash, operator, reason string) error {
	if env == "" {
		return envstate.ErrEnvRequired
	}
	if h == "" {
		return envstate.ErrHashRequired
	}
	if operator == "" {
		return envstate.ErrOperatorRequired
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("fsstate: begin promote_challenger tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	nowMS, err := monotonicAtMS(ctx, tx, env, s.clock())
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO env_state(env, challenger_hash, challenger_by, challenger_at, updated_at)
		      VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(env) DO UPDATE SET
		     challenger_hash = excluded.challenger_hash,
		     challenger_by   = excluded.challenger_by,
		     challenger_at   = excluded.challenger_at,
		     updated_at      = excluded.updated_at`,
		env, string(h), operator, nowMS, nowMS,
	); err != nil {
		return fmt.Errorf("fsstate: write env_state: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO env_history(env, kind, to_hash, operator, reason, at)
		      VALUES (?, ?, ?, ?, ?, ?)`,
		env, string(envstate.KindChallengerPromoted), string(h), operator, reason, nowMS,
	); err != nil {
		return fmt.Errorf("fsstate: write env_history: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("fsstate: commit promote_challenger tx: %w", err)
	}
	return nil
}

// RejectChallenger implements envstate.Writer (ADR-0009).
func (s *Store) RejectChallenger(ctx context.Context, env, operator, reason string) error {
	if env == "" {
		return envstate.ErrEnvRequired
	}
	if operator == "" {
		return envstate.ErrOperatorRequired
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("fsstate: begin reject_challenger tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var current sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT challenger_hash FROM env_state WHERE env = ?`, env).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) || !current.Valid {
			return envstate.ErrNoChallenger
		}
		return fmt.Errorf("fsstate: read challenger: %w", err)
	}
	if !current.Valid || current.String == "" {
		return envstate.ErrNoChallenger
	}

	nowMS, err := monotonicAtMS(ctx, tx, env, s.clock())
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE env_state
		    SET challenger_hash = NULL, challenger_by = NULL, challenger_at = NULL, updated_at = ?
		  WHERE env = ?`, nowMS, env,
	); err != nil {
		return fmt.Errorf("fsstate: update env_state: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO env_history(env, kind, from_hash, operator, reason, at)
		      VALUES (?, ?, ?, ?, ?, ?)`,
		env, string(envstate.KindChallengerRejected), current.String, operator, reason, nowMS,
	); err != nil {
		return fmt.Errorf("fsstate: write env_history: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("fsstate: commit reject_challenger tx: %w", err)
	}
	return nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// monotonicAtMS returns now.UnixMilli, but bumps strictly past any
// existing env_history.at for env. Mirrors memstate's clock-inside-
// lock posture: two writes in the same millisecond would otherwise
// collide on the (env, at) PK; here we keep history.at strictly
// increasing per env so the timestamp doubles as the cursor key.
func monotonicAtMS(ctx context.Context, tx *sql.Tx, env string, now time.Time) (int64, error) {
	nowMS := now.UnixMilli()
	var last sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT MAX(at) FROM env_history WHERE env = ?`, env).Scan(&last); err != nil {
		return 0, fmt.Errorf("fsstate: monotonic at lookup: %w", err)
	}
	if last.Valid && nowMS <= last.Int64 {
		nowMS = last.Int64 + 1
	}
	return nowMS, nil
}
