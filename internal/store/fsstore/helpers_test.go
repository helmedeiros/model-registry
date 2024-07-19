package fsstore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/store"
	"github.com/helmedeiros/model-registry/internal/store/fsstore"
)

// newFsstore opens an fsstore against a fresh t.TempDir() with a fake
// 1ms-per-call clock, so any test that depends on created_at ordering
// resolves deterministically without sleeping.
func newFsstore(t *testing.T) *fsstore.Store {
	t.Helper()
	clk := &fakeClock{now: time.Unix(0, 0).UTC()}
	s, err := fsstore.New(t.TempDir(), fsstore.WithClock(clk.Now))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time {
	c.now = c.now.Add(time.Millisecond)
	return c.now
}

func putRule(t *testing.T, s *fsstore.Store, name string) store.Hash {
	t.Helper()
	h, err := s.Put(context.Background(), store.PutRequest{
		SourceBytes: []byte("rule=" + name),
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedBy: "tester"},
	})
	if err != nil {
		t.Fatalf("Put(%s): %v", name, err)
	}
	return h
}

// reopenMetadataDB opens a second handle on the metadata.db file so
// fsstore-only tests can assert the on-disk row shape without going
// through the typed Store contract.
func reopenMetadataDB(t *testing.T, s *fsstore.Store) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(s.Root(), "metadata.db"))
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
