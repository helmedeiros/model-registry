//go:build bench

// Substrate micro-benches against the fsstore backing. Run with:
//
//	make bench-substrate
//
// Bars are pre-registered in scientific/v0.0.1/REPORT.md; measured
// numbers from a recent run land in the "Measured numbers" section.
package e2e

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/store"
	"github.com/helmedeiros/model-registry/internal/store/fsstore"
)

func newBenchStore(b *testing.B) *fsstore.Store {
	b.Helper()
	s, err := fsstore.New(b.TempDir())
	if err != nil {
		b.Fatalf("fsstore.New: %v", err)
	}
	b.Cleanup(func() { _ = s.Close() })
	return s
}

func mkBytes(n int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	out := make([]byte, n)
	_, _ = r.Read(out)
	return out
}

func BenchmarkStorePut_SmallArtifact(b *testing.B) {
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		s := newBenchStore(b)
		src := mkBytes(10*1024, int64(i))
		b.StartTimer()
		if _, err := s.Put(ctx, store.PutRequest{
			SourceBytes: src,
			ContentType: store.ContentTypeCSV,
			Metadata:    store.Metadata{CreatedBy: "bench"},
		}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStorePut_SmallArtifact_AllMembers(b *testing.B) {
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		s := newBenchStore(b)
		src := mkBytes(10*1024, int64(i))
		snap := mkBytes(8*1024, int64(i+1))
		diag := mkBytes(1024, int64(i+2))
		b.StartTimer()
		if _, err := s.Put(ctx, store.PutRequest{
			SourceBytes:   src,
			ContentType:   store.ContentTypeCSV,
			SnapshotBytes: snap,
			DiagnoseBytes: diag,
			Metadata:      store.Metadata{CreatedBy: "bench"},
		}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStorePut_LargeArtifact(b *testing.B) {
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		s := newBenchStore(b)
		src := mkBytes(2*1024*1024, int64(i))
		b.StartTimer()
		if _, err := s.Put(ctx, store.PutRequest{
			SourceBytes: src,
			ContentType: store.ContentTypeCSV,
			Metadata:    store.Metadata{CreatedBy: "bench"},
		}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStoreGetBundle(b *testing.B) {
	ctx := context.Background()
	s := newBenchStore(b)
	h, err := s.Put(ctx, store.PutRequest{
		SourceBytes: mkBytes(10*1024, 0),
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedBy: "bench"},
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.GetBundle(ctx, h); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStoreGetMember_SmallSourceWarm(b *testing.B) {
	ctx := context.Background()
	s := newBenchStore(b)
	h, err := s.Put(ctx, store.PutRequest{
		SourceBytes: mkBytes(10*1024, 0),
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedBy: "bench"},
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := s.GetMember(ctx, h, store.MemberSource); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStoreGetMember_LargeSource(b *testing.B) {
	ctx := context.Background()
	s := newBenchStore(b)
	h, err := s.Put(ctx, store.PutRequest{
		SourceBytes: mkBytes(2*1024*1024, 0),
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedBy: "bench"},
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := s.GetMember(ctx, h, store.MemberSource); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStoreTag(b *testing.B) {
	ctx := context.Background()
	s := newBenchStore(b)
	hashes := make([]store.Hash, b.N+1)
	for i := range hashes {
		h, err := s.Put(ctx, store.PutRequest{
			SourceBytes: mkBytes(1024, int64(i)),
			ContentType: store.ContentTypeCSV,
			Metadata:    store.Metadata{CreatedBy: "bench"},
		})
		if err != nil {
			b.Fatal(err)
		}
		hashes[i] = h
	}
	b.ResetTimer()
	// Unique tag name per iteration: production re-tagging happens at
	// human pace and never collides on (tag, assigned_at). The bench
	// loop's sub-millisecond cadence would.
	for i := 0; i < b.N; i++ {
		if err := s.Tag(ctx, fmt.Sprintf("head-%d", i), hashes[i]); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStoreResolveTag(b *testing.B) {
	ctx := context.Background()
	s := newBenchStore(b)
	h, err := s.Put(ctx, store.PutRequest{
		SourceBytes: mkBytes(1024, 0),
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedBy: "bench"},
	})
	if err != nil {
		b.Fatal(err)
	}
	if err := s.Tag(ctx, "release", h); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.ResolveTag(ctx, "release"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStoreListTags_1000Tags(b *testing.B) {
	ctx := context.Background()
	s := newBenchStore(b)
	for i := 0; i < 1000; i++ {
		h, err := s.Put(ctx, store.PutRequest{
			SourceBytes: mkBytes(256, int64(i)),
			ContentType: store.ContentTypeCSV,
			Metadata:    store.Metadata{CreatedBy: "bench", CreatedAt: time.Unix(int64(i), 0)},
		})
		if err != nil {
			b.Fatal(err)
		}
		if err := s.Tag(ctx, fmt.Sprintf("tag-%04d", i), h); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := s.ListTags(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if len(out) != 1000 {
			b.Fatalf("expected 1000 heads, got %d", len(out))
		}
	}
}
