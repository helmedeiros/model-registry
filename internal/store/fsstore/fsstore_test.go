package fsstore_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/store"
	"github.com/helmedeiros/model-registry/internal/store/fsstore"
	"github.com/helmedeiros/model-registry/internal/store/storetest"
)

func TestConformance(t *testing.T) {
	storetest.RunConformance(t, func(t *testing.T, clock func() time.Time) store.Store {
		s, err := fsstore.New(t.TempDir(), fsstore.WithClock(clock))
		if err != nil {
			t.Fatalf("fsstore.New: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

func TestNewRequiresNonEmptyRoot(t *testing.T) {
	if _, err := fsstore.New(""); err == nil {
		t.Fatal("expected error for empty root, got nil")
	}
}

func TestNewBootstrapsAFreshRoot(t *testing.T) {
	root := t.TempDir()
	s, err := fsstore.New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if got := s.Root(); got != root {
		t.Fatalf("Root()=%q want %q", got, root)
	}
	if _, err := os.Stat(filepath.Join(root, "objects")); err != nil {
		t.Fatalf("objects dir not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "metadata.db")); err != nil {
		t.Fatalf("metadata.db not created: %v", err)
	}
	ver, err := os.ReadFile(filepath.Join(root, "version.txt"))
	if err != nil {
		t.Fatalf("version.txt: %v", err)
	}
	if string(ver) != "2\n" {
		t.Fatalf("version.txt=%q want %q", ver, "2\n")
	}
}

func TestNewIsIdempotentOnExistingRoot(t *testing.T) {
	root := t.TempDir()
	s1, err := fsstore.New(root)
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	s2, err := fsstore.New(root)
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })
}

func TestNewAppliesSchemaWithBothTables(t *testing.T) {
	root := t.TempDir()
	s, err := fsstore.New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	db, err := sql.Open("sqlite", filepath.Join(root, "metadata.db"))
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, table := range []string{"artifacts", "tags"} {
		var name string
		err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?",
			table,
		).Scan(&name)
		if err != nil {
			t.Fatalf("schema missing table %q: %v", table, err)
		}
	}
	for _, index := range []string{"idx_artifacts_state_created", "idx_tags_current"} {
		var name string
		err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='index' AND name=?",
			index,
		).Scan(&name)
		if err != nil {
			t.Fatalf("schema missing index %q: %v", index, err)
		}
	}
}

func TestNewSetsWALJournalMode(t *testing.T) {
	root := t.TempDir()
	s, err := fsstore.New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// journal_mode is persisted in the SQLite file header. foreign_keys
	// and busy_timeout are per-connection; the DSN _pragma parameters
	// make the driver bind them on every connection the pool opens, so
	// every operation that goes through fsstore.New's *sql.DB sees them
	// even though they are not observable from a separately-opened
	// connection here.
	db, err := sql.Open("sqlite", filepath.Join(root, "metadata.db"))
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode=%q want wal", journalMode)
	}
}

func TestNewFailsWhenRootIsAFile(t *testing.T) {
	tmp := t.TempDir()
	bogus := filepath.Join(tmp, "blocked")
	if err := os.WriteFile(bogus, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fsstore.New(bogus); err == nil {
		t.Fatal("expected error when root path is a file, got nil")
	}
}

