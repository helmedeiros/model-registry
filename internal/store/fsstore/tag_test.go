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

func countTagRows(t *testing.T, s *fsstore.Store, tag string) int {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(s.Root(), "metadata.db"))
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tags WHERE tag = ?`, tag).Scan(&n); err != nil {
		t.Fatalf("count tag rows: %v", err)
	}
	return n
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

func TestTagTransitionsStagedToActiveAndRecordsHead(t *testing.T) {
	s := newFsstore(t)
	h := putRule(t, s, "alpha")
	if err := s.Tag(context.Background(), "v1", h); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	bun, err := s.GetBundle(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if bun.State != store.StateActive {
		t.Fatalf("state=%s want active", bun.State)
	}
	got, err := s.ResolveTag(context.Background(), "v1")
	if err != nil || got != h {
		t.Fatalf("ResolveTag(v1)=%s err=%v want=%s", got, err, h)
	}
}

func TestTagRePointingMovesHeadAndKeepsHistory(t *testing.T) {
	s := newFsstore(t)
	h1 := putRule(t, s, "v1-bytes")
	h2 := putRule(t, s, "v2-bytes")
	if err := s.Tag(context.Background(), "release", h1); err != nil {
		t.Fatal(err)
	}
	if err := s.Tag(context.Background(), "release", h2); err != nil {
		t.Fatal(err)
	}
	got, err := s.ResolveTag(context.Background(), "release")
	if err != nil || got != h2 {
		t.Fatalf("ResolveTag(release)=%s err=%v want=%s", got, err, h2)
	}
	// Append-only: both rows must remain on disk so a later audit
	// read-model can answer "what did release point at last Tuesday?".
	count := countTagRows(t, s, "release")
	if count != 2 {
		t.Fatalf("history rows for release=%d, want 2", count)
	}
}

func TestTagUnknownHashReturnsNotFound(t *testing.T) {
	s := newFsstore(t)
	if err := s.Tag(context.Background(), "v1", store.Hash("missing")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestResolveTagUnknownReturnsTagUnknown(t *testing.T) {
	s := newFsstore(t)
	if _, err := s.ResolveTag(context.Background(), "never"); !errors.Is(err, store.ErrTagUnknown) {
		t.Fatalf("expected ErrTagUnknown, got %v", err)
	}
}

func TestListTagsReturnsCurrentHeadsOnly(t *testing.T) {
	s := newFsstore(t)
	h1 := putRule(t, s, "first")
	h2 := putRule(t, s, "second")
	h3 := putRule(t, s, "third")
	if err := s.Tag(context.Background(), "a", h1); err != nil {
		t.Fatal(err)
	}
	if err := s.Tag(context.Background(), "b", h3); err != nil {
		t.Fatal(err)
	}
	if err := s.Tag(context.Background(), "a", h2); err != nil {
		t.Fatal(err)
	}
	heads, err := s.ListTags(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 2 || heads["a"] != h2 || heads["b"] != h3 {
		t.Fatalf("heads=%v want a=%s b=%s", heads, h2, h3)
	}
}
