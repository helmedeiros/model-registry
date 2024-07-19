package fsstore_test

import (
	"context"
	"testing"

	"github.com/helmedeiros/model-registry/internal/store"
	"github.com/helmedeiros/model-registry/internal/store/fsstore"
)

// TestTagRePointingAppendsHistoryRowOnDisk is the fsstore-only invariant:
// each Tag reassignment inserts a new (tag, hash, assigned_at) row rather
// than overwriting in place, so a later audit read-model can answer
// "what did this tag point at last Tuesday?". The conformance suite
// covers head semantics through ResolveTag; this test covers the
// append-only schema commitment ADR-0002 makes about the tags table.
// Row count alone would pass a regression where both rows recorded the
// same hash; asserting per-hash presence makes the schema commitment
// falsifiable.
func TestTagRePointingAppendsHistoryRowOnDisk(t *testing.T) {
	s := newFsstore(t)
	h1 := putRule(t, s, "v1-bytes")
	h2 := putRule(t, s, "v2-bytes")
	if err := s.Tag(context.Background(), "release", h1); err != nil {
		t.Fatal(err)
	}
	if err := s.Tag(context.Background(), "release", h2); err != nil {
		t.Fatal(err)
	}
	if count := countTagRows(t, s, "release"); count != 2 {
		t.Fatalf("history rows for release=%d, want 2", count)
	}
	for _, want := range []store.Hash{h1, h2} {
		if n := countTagRowsForHash(t, s, "release", want); n != 1 {
			t.Fatalf("expected exactly one (release, %s) row, got %d", want, n)
		}
	}
}

func countTagRows(t *testing.T, s *fsstore.Store, tag string) int {
	t.Helper()
	db := reopenMetadataDB(t, s)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tags WHERE tag = ?`, tag).Scan(&n); err != nil {
		t.Fatalf("count tag rows: %v", err)
	}
	return n
}

func countTagRowsForHash(t *testing.T, s *fsstore.Store, tag string, h store.Hash) int {
	t.Helper()
	db := reopenMetadataDB(t, s)
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM tags WHERE tag = ? AND hash = ?`, tag, string(h),
	).Scan(&n); err != nil {
		t.Fatalf("count tag rows by hash: %v", err)
	}
	return n
}
