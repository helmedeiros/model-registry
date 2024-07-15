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

// writeAtomic writes data via tempfile + fsync + rename. Parent dir is
// not fsynced — POSIX-strict durability is traded for the per-write
// fsync cost; a crash between rename and dir-block flush is recoverable
// by re-upload.
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
