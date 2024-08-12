package fsaudit

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/audit"
)

// BenchmarkFsauditList_100Entries probes the keyset List path against
// 100 seeded rows — the same depth ADR-0004's HTTP-layer
// BenchmarkGET_Audit_100Entries hits. The substrate bar is tighter:
// the handler measurement budget covers HTTP + handler + wire encode,
// so fsaudit on its own has to come in well under that.
// Pre-registered bar: < 5 ms / op against the (at, id) DESC index.
func BenchmarkFsauditList_100Entries(b *testing.B) {
	s := newBenchStore(b)
	seedEntries(b, s, 100)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.List(ctx, audit.ListOptions{Limit: 100}); err != nil {
			b.Fatalf("List: %v", err)
		}
	}
}

// BenchmarkFsauditRecord probes the INSERT path. Pre-registered bar:
// < 5 ms / op for the single-statement append. The PK conflict path is
// not on the hot loop so it is not benched.
func BenchmarkFsauditRecord(b *testing.B) {
	s := newBenchStore(b)
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entry := audit.Entry{
			ID:       fmt.Sprintf("01HXY%020d", i),
			Operator: "bench-bot",
			Action:   "promote",
			Target:   "env/bench",
			At:       base.Add(time.Duration(i) * time.Millisecond),
		}
		if err := s.Record(ctx, entry); err != nil {
			b.Fatalf("Record: %v", err)
		}
	}
}

func newBenchStore(b *testing.B) *Store {
	b.Helper()
	dir := b.TempDir()
	s, err := New(filepath.Join(dir, "audit.db"))
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	b.Cleanup(func() { _ = s.Close() })
	return s
}

func seedEntries(b *testing.B, s *Store, n int) {
	b.Helper()
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()
	for i := 0; i < n; i++ {
		entry := audit.Entry{
			ID:       fmt.Sprintf("01HXY%020d", i),
			Operator: "bench-bot",
			Action:   "promote",
			Target:   "env/bench",
			At:       base.Add(time.Duration(i) * time.Millisecond),
		}
		if err := s.Record(ctx, entry); err != nil {
			b.Fatalf("seed Record: %v", err)
		}
	}
}
