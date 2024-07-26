package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/helmedeiros/model-registry/internal/store"
)

func Artifact(reader store.Reader) http.Handler {
	if reader == nil {
		panic("httpapi.Artifact: reader required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		hash := store.Hash(r.PathValue("hash"))
		if hash == "" {
			writeError(w, http.StatusBadRequest, "missing_hash")
			return
		}
		bundle, err := reader.GetBundle(r.Context(), hash)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "get_failed")
			return
		}
		writeJSON(w, http.StatusOK, toArtifactBundle(bundle))
	})
}

// ArtifactMember serves GET /artifact/{hash}/{member}. Source bytes
// carry the artifact's declared Content-Type; derived members
// (snapshot, diagnose) carry application/octet-stream so callers
// branch on the path component, not the response header.
func ArtifactMember(reader store.Reader) http.Handler {
	if reader == nil {
		panic("httpapi.ArtifactMember: reader required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		hash := store.Hash(r.PathValue("hash"))
		if hash == "" {
			writeError(w, http.StatusBadRequest, "missing_hash")
			return
		}
		kind, err := parseMemberKind(r.PathValue("member"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		bytes, contentType, err := reader.GetMember(r.Context(), hash, kind)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if errors.Is(err, store.ErrMemberAbsent) {
			writeError(w, http.StatusNotFound, "member_absent")
			return
		}
		// Defensive: parseMemberKind already filters, but the substrate's
		// ErrInvalidKind exists as a typed error so callers can branch on
		// it. Catching it here preserves the 400 boundary instead of
		// surfacing a 500 if a future enum extension slips through.
		if errors.Is(err, store.ErrInvalidKind) {
			writeError(w, http.StatusBadRequest, "invalid_member")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "get_failed")
			return
		}
		w.Header().Set("X-Artifact-Hash", string(hash))
		w.Header().Set("Content-Length", strconv.Itoa(len(bytes)))
		w.Header().Set("Content-Type", responseContentType(kind, contentType))
		_, _ = w.Write(bytes)
	})
}

func parseMemberKind(s string) (store.MemberKind, error) {
	switch store.MemberKind(s) {
	case store.MemberSource, store.MemberSnapshot, store.MemberDiagnose:
		return store.MemberKind(s), nil
	default:
		return "", errors.New("invalid_member")
	}
}

func responseContentType(kind store.MemberKind, declared store.ContentType) string {
	if kind == store.MemberSource && declared != store.ContentTypeUnknown {
		return string(declared)
	}
	return "application/octet-stream"
}

func toArtifactBundle(b store.Bundle) ArtifactBundle {
	return ArtifactBundle{
		Hash:        string(b.Hash),
		ContentType: string(b.ContentType),
		State:       string(b.State),
		Metadata: ArtifactMetaJSON{
			CreatedAt:        b.Metadata.CreatedAt.UTC().Format(time.RFC3339Nano),
			CreatedBy:        b.Metadata.CreatedBy,
			SourceCommitSHA:  b.Metadata.SourceCommitSHA,
			Description:      b.Metadata.Description,
			DerivedByVersion: b.Metadata.DerivedByVersion,
		},
		HasSnapshot: b.HasSnapshot,
		HasDiagnose: b.HasDiagnose,
	}
}
