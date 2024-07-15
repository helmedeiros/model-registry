package fsstore_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/store"
	"github.com/helmedeiros/model-registry/internal/store/fsstore"
)

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

func TestPutWritesShardedDirAndLandsBytes(t *testing.T) {
	s := newFsstore(t)

	source := []byte("alpha,rule,1.0,1\n")
	h, err := s.Put(context.Background(), store.PutRequest{
		SourceBytes:   source,
		ContentType:   store.ContentTypeCSV,
		SnapshotBytes: []byte("snap"),
		Metadata:      store.Metadata{CreatedBy: "tester"},
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	sum := sha256.Sum256(source)
	want := store.Hash(hex.EncodeToString(sum[:]))
	if h != want {
		t.Fatalf("Put returned hash %s, want %s", h, want)
	}

	dir := filepath.Join(s.Root(), "objects", string(h)[:2], string(h))
	for _, member := range []string{"source", "snapshot", "metadata.json"} {
		p := filepath.Join(dir, member)
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("member %s missing on disk: %v", member, err)
		}
		if fi.Size() == 0 {
			t.Fatalf("member %s is empty on disk", member)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "diagnose")); !os.IsNotExist(err) {
		t.Fatalf("unexpected diagnose file on disk: %v", err)
	}
}

func TestPutIsIdempotentAndDoesNotOverwriteMetadata(t *testing.T) {
	s := newFsstore(t)

	first, err := s.Put(context.Background(), store.PutRequest{
		SourceBytes: []byte("payload"),
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedBy: "first"},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Put(context.Background(), store.PutRequest{
		SourceBytes: []byte("payload"),
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedBy: "second-ignored", Description: "ignored"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("re-Put returned %s, want %s", second, first)
	}
	bun, err := s.GetBundle(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if bun.Metadata.CreatedBy != "first" || bun.Metadata.Description != "" {
		t.Fatalf("metadata overwritten on re-Put: %+v", bun.Metadata)
	}
}

func TestPutValidatesRequiredFields(t *testing.T) {
	s := newFsstore(t)
	if _, err := s.Put(context.Background(), store.PutRequest{ContentType: store.ContentTypeCSV}); !errors.Is(err, store.ErrSourceRequired) {
		t.Fatalf("expected ErrSourceRequired, got %v", err)
	}
	if _, err := s.Put(context.Background(), store.PutRequest{SourceBytes: []byte("x")}); !errors.Is(err, store.ErrContentTypeRequired) {
		t.Fatalf("expected ErrContentTypeRequired, got %v", err)
	}
}

func TestPutDoesNotRetainCallerSliceReference(t *testing.T) {
	s := newFsstore(t)
	src := []byte("original")
	h, err := s.Put(context.Background(), store.PutRequest{SourceBytes: src, ContentType: store.ContentTypeCSV})
	if err != nil {
		t.Fatal(err)
	}
	src[0] = 'X'
	got, _, err := s.GetMember(context.Background(), h, store.MemberSource)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("Store retained reference to caller slice: %q", got)
	}
}
