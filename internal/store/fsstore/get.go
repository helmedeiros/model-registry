package fsstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/helmedeiros/model-registry/internal/store"
)

// GetBundle implements store.Reader.
func (s *Store) GetBundle(ctx context.Context, h store.Hash) (store.Bundle, error) {
	var (
		hash, contentType, state, createdBy string
		commitSHA, description, derivedBy   sql.NullString
		createdAtMS                         int64
		hasSnapshot, hasDiagnose            int
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT hash, content_type, state, created_at, created_by,
		        source_commit_sha, description, derived_by_version,
		        has_snapshot, has_diagnose
		 FROM artifacts WHERE hash = ?`, string(h)).
		Scan(&hash, &contentType, &state, &createdAtMS, &createdBy,
			&commitSHA, &description, &derivedBy,
			&hasSnapshot, &hasDiagnose)
	if isNoRows(err) {
		return store.Bundle{}, store.ErrNotFound
	}
	if err != nil {
		return store.Bundle{}, fmt.Errorf("fsstore: select bundle: %w", err)
	}
	return store.Bundle{
		Hash:        store.Hash(hash),
		ContentType: store.ContentType(contentType),
		State:       store.State(state),
		Metadata: store.Metadata{
			CreatedAt:        time.UnixMilli(createdAtMS).UTC(),
			CreatedBy:        createdBy,
			SourceCommitSHA:  commitSHA.String,
			Description:      description.String,
			DerivedByVersion: derivedBy.String,
		},
		HasSnapshot: hasSnapshot == 1,
		HasDiagnose: hasDiagnose == 1,
	}, nil
}

// GetMember implements store.Reader.
func (s *Store) GetMember(ctx context.Context, h store.Hash, m store.MemberKind) ([]byte, store.ContentType, error) {
	var (
		contentType              string
		hasSnapshot, hasDiagnose int
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT content_type, has_snapshot, has_diagnose FROM artifacts WHERE hash = ?`,
		string(h)).Scan(&contentType, &hasSnapshot, &hasDiagnose)
	if isNoRows(err) {
		return nil, "", store.ErrNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("fsstore: select member: %w", err)
	}

	var (
		path     string
		returnCT store.ContentType
	)
	switch m {
	case store.MemberSource:
		path = s.memberPath(h, memberSource)
		returnCT = store.ContentType(contentType)
	case store.MemberSnapshot:
		if hasSnapshot != 1 {
			return nil, "", store.ErrMemberAbsent
		}
		path = s.memberPath(h, memberSnapshot)
		returnCT = store.ContentTypeUnknown
	case store.MemberDiagnose:
		if hasDiagnose != 1 {
			return nil, "", store.ErrMemberAbsent
		}
		path = s.memberPath(h, memberDiagnose)
		returnCT = store.ContentTypeUnknown
	default:
		return nil, "", store.ErrMemberAbsent
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("fsstore: read member: %w", err)
	}
	return data, returnCT, nil
}

func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
