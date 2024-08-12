package fsaudit

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/audit/audittest"
)

func TestConformance(t *testing.T) {
	audittest.RunConformance(t, func(t *testing.T) (audit.Store, audittest.SeedFunc) {
		dir := t.TempDir()
		s, err := New(filepath.Join(dir, "audit.db"))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		seed := func(entries []audit.Entry) {
			for _, e := range entries {
				if _, err := s.db.Exec(
					`INSERT INTO audit_entry(id, operator, action, target, artifact_hash, reason, at)
					      VALUES (?, ?, ?, ?, ?, ?, ?)`,
					e.ID, e.Operator, e.Action, e.Target,
					nullableString(string(e.ArtifactHash)),
					nullableString(e.Reason),
					e.At.UnixMilli(),
				); err != nil {
					t.Fatalf("seed audit_entry: %v", err)
				}
			}
		}
		return s, seed
	})
}

func TestNewRequiresPath(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("expected error for empty path")
	}
}

// TestNewIsIdempotent covers durability + sort-order survival through
// one Record/Close/New/List cycle. A second entry is written so the
// reopened backing has to compare two rows, not just preserve one.
func TestNewIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.db")
	s1, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	older := audit.Entry{
		ID:       "01HXYA",
		Operator: "ci-bot",
		Action:   "promote_champion",
		Target:   "env=production",
		At:       time.Unix(1_700_000_000, 0).UTC(),
	}
	newer := audit.Entry{
		ID:       "01HXYB",
		Operator: "ci-bot",
		Action:   "promote_champion",
		Target:   "env=production",
		At:       time.Unix(1_700_000_001, 0).UTC(),
	}
	for _, e := range []audit.Entry{older, newer} {
		if err := s1.Record(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	page, err := s2.List(context.Background(), audit.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("entries lost across reopen: %+v", page.Items)
	}
	if page.Items[0].ID != newer.ID || page.Items[1].ID != older.ID {
		t.Fatalf("sort order lost across reopen: %+v", page.Items)
	}
}
