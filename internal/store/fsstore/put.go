package fsstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/helmedeiros/model-registry/internal/store"
)

// Put implements store.Writer.
func (s *Store) Put(ctx context.Context, req store.PutRequest) (store.Hash, error) {
	if err := req.Validate(); err != nil {
		return "", err
	}
	h := hashOf(req.SourceBytes)

	// Probe short-circuits re-Puts before any file IO; the INSERT OR
	// IGNORE below is the authoritative race resolver.
	var found string
	switch err := s.db.QueryRowContext(ctx, `SELECT hash FROM artifacts WHERE hash = ?`, string(h)).Scan(&found); {
	case err == nil:
		return h, nil
	case !isNoRows(err):
		return "", fmt.Errorf("fsstore: probe existing: %w", err)
	}

	md := req.Metadata
	if md.CreatedAt.IsZero() {
		md.CreatedAt = s.clock()
	}

	dir := s.artifactDir(h)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("fsstore: mkdir artifact: %w", err)
	}

	if err := writeAtomic(filepath.Join(dir, memberSource), req.SourceBytes); err != nil {
		return "", err
	}
	hasSnapshot := len(req.SnapshotBytes) > 0
	if hasSnapshot {
		if err := writeAtomic(filepath.Join(dir, memberSnapshot), req.SnapshotBytes); err != nil {
			return "", err
		}
	}
	hasDiagnose := len(req.DiagnoseBytes) > 0
	if hasDiagnose {
		if err := writeAtomic(filepath.Join(dir, memberDiagnose), req.DiagnoseBytes); err != nil {
			return "", err
		}
	}
	mdJSON, err := json.Marshal(md)
	if err != nil {
		return "", fmt.Errorf("fsstore: marshal metadata: %w", err)
	}
	if err := writeAtomic(filepath.Join(dir, memberMetadata), mdJSON); err != nil {
		return "", err
	}

	// Race loser's INSERT silently dropped; their identical
	// content-addressed bytes are already on disk.
	_, err = s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO artifacts
			(hash, content_type, state, created_at, created_by,
			 source_commit_sha, description, derived_by_version,
			 has_snapshot, has_diagnose)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(h), string(req.ContentType), string(store.StateStaged),
		md.CreatedAt.UnixMilli(), md.CreatedBy,
		nullable(md.SourceCommitSHA), nullable(md.Description), nullable(md.DerivedByVersion),
		boolToInt(hasSnapshot), boolToInt(hasDiagnose),
	)
	if err != nil {
		return "", fmt.Errorf("fsstore: insert artifact: %w", err)
	}
	return h, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
