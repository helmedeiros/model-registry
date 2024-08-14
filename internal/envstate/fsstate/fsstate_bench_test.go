package fsstate

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/envstate"
	"github.com/helmedeiros/model-registry/internal/store"
)

// BenchmarkFsstateHistory_100Entries probes the keyset History path
// against 100 seeded transitions — same depth ADR-0004's HTTP-layer
// BenchmarkGET_EnvHistory_100Entries hits. The substrate bar is
// tighter because the handler measurement budget covers HTTP +
// handler + wire encode.
// Pre-registered bar: < 5 ms / op against the (env, at DESC) index.
func BenchmarkFsstateHistory_100Entries(b *testing.B) {
	s := newBenchStore(b)
	seedTransitions(b, s, "production", 100)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.History(ctx, "production", envstate.ListOptions{Limit: 100}); err != nil {
			b.Fatalf("History: %v", err)
		}
	}
}

// BenchmarkFsstatePromoteChampion probes the write tx that does
// (1) MAX(at) lookup, (2) UPSERT env_state, (3) INSERT env_history.
// Pre-registered bar: < 5 ms / op.
func BenchmarkFsstatePromoteChampion(b *testing.B) {
	s := newBenchStore(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h := store.Hash(fmt.Sprintf("h%020d", i))
		if _, err := s.PromoteChampion(ctx, "production", h, "bench-bot", "tick"); err != nil {
			b.Fatalf("PromoteChampion: %v", err)
		}
	}
}

// BenchmarkFsstateRollbackChampion probes the write tx that does
// (1) read current champion, (2) read prior champion, (3) UPDATE
// env_state, (4) INSERT env_history. A pre-seeded two-hash history
// keeps every iteration valid (rollback to the other hash).
// Pre-registered bar: < 5 ms / op.
func BenchmarkFsstateRollbackChampion(b *testing.B) {
	s := newBenchStore(b)
	ctx := context.Background()
	// Seed two distinct champion_promoted entries so RollbackChampion
	// has a prior to fall back to on every iteration. We alternate
	// promotes between hashA and hashB so the rollback target stays
	// distinct from the current champion.
	hashA := store.Hash("hA")
	hashB := store.Hash("hB")
	if _, err := s.PromoteChampion(ctx, "production", hashA, "bench-bot", "seed-a"); err != nil {
		b.Fatalf("seed A: %v", err)
	}
	if _, err := s.PromoteChampion(ctx, "production", hashB, "bench-bot", "seed-b"); err != nil {
		b.Fatalf("seed B: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.RollbackChampion(ctx, "production", "bench-bot", "tick"); err != nil {
			b.Fatalf("RollbackChampion: %v", err)
		}
	}
}

func newBenchStore(b *testing.B) *Store {
	b.Helper()
	dir := b.TempDir()
	s, err := New(filepath.Join(dir, "envstate.db"))
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	b.Cleanup(func() { _ = s.Close() })
	return s
}

func seedTransitions(b *testing.B, s *Store, env string, n int) {
	b.Helper()
	base := time.Unix(1_700_000_000, 0).UTC()
	for i := 0; i < n; i++ {
		_, err := s.db.Exec(
			`INSERT INTO env_history(env, kind, from_hash, to_hash, operator, reason, at)
			      VALUES (?, ?, ?, ?, ?, ?, ?)`,
			env, string(envstate.KindChampionPromoted),
			"", fmt.Sprintf("h%020d", i),
			"bench-bot", "seed",
			base.Add(time.Duration(i)*time.Millisecond).UnixMilli(),
		)
		if err != nil {
			b.Fatalf("seed env_history: %v", err)
		}
	}
}
