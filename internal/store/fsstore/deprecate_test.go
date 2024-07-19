package fsstore_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/helmedeiros/model-registry/internal/store"
	"github.com/helmedeiros/model-registry/internal/store/fsstore"
)

func TestDeprecateFromStagedIsTerminal(t *testing.T) {
	s := newFsstore(t)
	h := putRule(t, s, "alpha")

	if err := s.Deprecate(context.Background(), h, "withdrawn"); err != nil {
		t.Fatalf("Deprecate: %v", err)
	}
	bun, err := s.GetBundle(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if bun.State != store.StateDeprecated {
		t.Fatalf("state=%s want deprecated", bun.State)
	}
	if err := s.Deprecate(context.Background(), h, "again"); !errors.Is(err, store.ErrInvalidTransition) {
		t.Fatalf("re-deprecation should fail, got %v", err)
	}
}

func TestDeprecateFromActiveIsTerminal(t *testing.T) {
	s := newFsstore(t)
	h := putRule(t, s, "alpha")
	if err := s.Tag(context.Background(), "v1", h); err != nil {
		t.Fatal(err)
	}
	if err := s.Deprecate(context.Background(), h, "rolled out"); err != nil {
		t.Fatalf("Deprecate: %v", err)
	}
	bun, err := s.GetBundle(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if bun.State != store.StateDeprecated {
		t.Fatalf("state=%s want deprecated", bun.State)
	}
}

func TestDeprecateUnknownHashReturnsNotFound(t *testing.T) {
	s := newFsstore(t)
	if err := s.Deprecate(context.Background(), store.Hash("missing"), "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDeprecateRecordsTimestampAndReasonOnDisk(t *testing.T) {
	s := newFsstore(t)
	h := putRule(t, s, "alpha")

	if err := s.Deprecate(context.Background(), h, "obsolete-by-policy"); err != nil {
		t.Fatal(err)
	}
	if ts, reason := readDeprecationRow(t, s, h); ts == 0 || reason != "obsolete-by-policy" {
		t.Fatalf("deprecation row: ts=%d reason=%q", ts, reason)
	}
}

func readDeprecationRow(t *testing.T, s *fsstore.Store, h store.Hash) (int64, string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(s.Root(), "metadata.db"))
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var (
		ts     sql.NullInt64
		reason sql.NullString
	)
	if err := db.QueryRow(
		`SELECT deprecated_at, deprecated_reason FROM artifacts WHERE hash = ?`,
		string(h),
	).Scan(&ts, &reason); err != nil {
		t.Fatalf("read deprecation row: %v", err)
	}
	return ts.Int64, reason.String
}
