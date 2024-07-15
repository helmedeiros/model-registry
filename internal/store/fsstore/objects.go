package fsstore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/helmedeiros/model-registry/internal/store"
)

const (
	memberSource   = "source"
	memberSnapshot = "snapshot"
	memberDiagnose = "diagnose"
	memberMetadata = "metadata.json"
)

func hashOf(b []byte) store.Hash {
	sum := sha256.Sum256(b)
	return store.Hash(hex.EncodeToString(sum[:]))
}

// artifactDir returns the on-disk directory for a hash, sharded by the
// first two hex characters so a single directory never holds millions of
// entries. Same pattern Git uses.
func (s *Store) artifactDir(h store.Hash) string {
	hh := string(h)
	return filepath.Join(s.root, objectsDir, hh[:2], hh)
}

func (s *Store) memberPath(h store.Hash, member string) string {
	return filepath.Join(s.artifactDir(h), member)
}

// writeAtomic writes data to a fresh temp file in the destination's
// directory, fsyncs it, and renames over the destination. Either the
// destination contains the new bytes or it is unchanged — no partial
// state on crash. Caller must ensure the parent directory exists.
//
// Known gap: the parent directory is not fsynced after the rename. POSIX
// strictly requires that to durably persist the new dentry across a
// crash. v1's single-process single-writer posture treats this as an
// acceptable risk against the per-write fsync cost (~3 ms on SSD); a
// crash between rename and dir-block flush would orphan the temp bytes
// while leaving the destination absent — recoverable by re-uploading.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp.")
	if err != nil {
		return fmt.Errorf("fsstore: create temp: %w", err)
	}
	tmpPath := f.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsstore: write temp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsstore: fsync temp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("fsstore: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("fsstore: rename: %w", err)
	}
	return nil
}
