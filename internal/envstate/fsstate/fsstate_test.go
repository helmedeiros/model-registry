package fsstate

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/envstate"
	"github.com/helmedeiros/model-registry/internal/envstate/envstatetest"
)

func TestConformance(t *testing.T) {
	envstatetest.RunConformance(t, func(t *testing.T) (envstate.Store, envstatetest.SeedFunc) {
		dir := t.TempDir()
		s, err := New(filepath.Join(dir, "envstate.db"))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })

		// SeedFunc reaches into the DB directly so the conformance
		// fixtures populate without going through Writer (some Reader
		// cases must run with state the Writer would not normally
		// produce, e.g. seeded state with no transitions).
		seed := func(state envstate.State, history []envstate.Transition) {
			if state.Env != "" {
				var championHash, championBy any
				var championAt any
				if state.Champion != nil {
					championHash = string(state.Champion.Hash)
					championBy = state.Champion.PromotedBy
					championAt = state.Champion.PromotedAt.UnixMilli()
				}
				var challengerHash, challengerBy any
				var challengerAt any
				if state.Challenger != nil {
					challengerHash = string(state.Challenger.Hash)
					challengerBy = state.Challenger.PromotedBy
					challengerAt = state.Challenger.PromotedAt.UnixMilli()
				}
				var updatedAt any
				if !state.UpdatedAt.IsZero() {
					updatedAt = state.UpdatedAt.UnixMilli()
				}
				if _, err := s.db.Exec(
					`INSERT INTO env_state(env, champion_hash, champion_by, champion_at, challenger_hash, challenger_by, challenger_at, updated_at)
					      VALUES (?, ?, ?, ?, ?, ?, ?, ?)
					 ON CONFLICT(env) DO UPDATE SET
					     champion_hash = excluded.champion_hash,
					     champion_by   = excluded.champion_by,
					     champion_at   = excluded.champion_at,
					     challenger_hash = excluded.challenger_hash,
					     challenger_by   = excluded.challenger_by,
					     challenger_at   = excluded.challenger_at,
					     updated_at = excluded.updated_at`,
					state.Env, championHash, championBy, championAt, challengerHash, challengerBy, challengerAt, updatedAt,
				); err != nil {
					t.Fatalf("seed env_state: %v", err)
				}
			}
			for _, tr := range history {
				if _, err := s.db.Exec(
					`INSERT INTO env_history(env, kind, from_hash, to_hash, operator, reason, at)
					      VALUES (?, ?, ?, ?, ?, ?, ?)`,
					tr.Env, string(tr.Kind), string(tr.FromHash), string(tr.ToHash),
					tr.Operator, tr.Reason, tr.At.UnixMilli(),
				); err != nil {
					t.Fatalf("seed env_history: %v", err)
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

func TestNewIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "envstate.db")
	s1, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.PromoteChampion(context.Background(), "production", "h1", "ci-bot", ""); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := New(path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	got, err := s2.Get(context.Background(), "production")
	if err != nil {
		t.Fatal(err)
	}
	if got.Champion == nil || got.Champion.Hash != "h1" {
		t.Fatalf("state lost across reopen: %+v", got)
	}
}

func TestPromoteAndHistoryPersistedOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "envstate.db")
	s, err := New(path, WithClock(func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if _, err := s.PromoteChampion(context.Background(), "production", "h1", "ci-bot", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromoteChampion(context.Background(), "production", "h2", "ci-bot", "weekly"); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", filepath.Join(dir, "envstate.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var rowCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM env_history WHERE env = ?`, "production").Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 2 {
		t.Fatalf("env_history rows=%d want 2", rowCount)
	}
}
