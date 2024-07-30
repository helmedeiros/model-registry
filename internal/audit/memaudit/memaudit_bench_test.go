package memaudit_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/audit/memaudit"
)

// BenchmarkListSize_100Entries pins the per-call cost of List at the
// page-fits-in-store regime ADR-0004 commits the < 50 ms bar against.
func BenchmarkListSize_100Entries(b *testing.B) {
	s := seededStore(b, 100)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.List(ctx, audit.ListOptions{Limit: 100}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkListSize_1000Entries reads the cost when the in-memory
// store carries 10× the page size. The defensive-copy strategy means
// per-call cost grows linearly with store depth — this bench is the
// honest number, not a speculation.
func BenchmarkListSize_1000Entries(b *testing.B) {
	s := seededStore(b, 1000)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.List(ctx, audit.ListOptions{Limit: 100}); err != nil {
			b.Fatal(err)
		}
	}
}

func seededStore(b *testing.B, n int) *memaudit.Store {
	b.Helper()
	s := memaudit.New()
	seed := memaudit.SeedFromTestPackage(s)
	entries := make([]audit.Entry, n)
	for i := range entries {
		entries[i] = audit.Entry{
			ID:       strconv.Itoa(i),
			Operator: "alice",
			Action:   "promote",
			Target:   "env/production/champion",
			At:       time.Unix(int64(i), 0).UTC(),
		}
	}
	seed(entries)
	return s
}
