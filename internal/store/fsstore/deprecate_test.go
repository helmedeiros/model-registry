package fsstore_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/helmedeiros/model-registry/internal/store"
	"github.com/helmedeiros/model-registry/internal/store/fsstore"
)

// TestDeprecateRecordsTimestampAndReasonOnDisk is the fsstore-only
// invariant: deprecated_at + deprecated_reason land in the artifacts
// row so the audit read-model can answer "when and why was this
// deprecated?". The conformance suite covers the typed-contract
// behavior (state transition, terminality, ErrNotFound on unknown
// hash); this test covers the on-disk column commitment.
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
	db := reopenMetadataDB(t, s)
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
