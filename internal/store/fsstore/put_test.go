package fsstore_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/helmedeiros/model-registry/internal/store"
)

// TestPutWritesShardedDirAndLandsBytes is the fsstore-only invariant:
// the on-disk layout is `<root>/objects/<hash[:2]>/<hash>/{source,
// snapshot, metadata.json}`, derived members appear only when bytes
// were supplied, and the SHA-256 of the source bytes is the hash. The
// conformance suite covers Put's typed-contract behavior; this test
// covers the filesystem shape it commits to.
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
