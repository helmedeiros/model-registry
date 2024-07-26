package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/helmedeiros/model-registry/internal/store"
)

// ArtifactSummary is the wire envelope for store.Summary. Kept
// separate so the public JSON shape is stable against future fields
// landing on the internal struct.
type ArtifactSummary struct {
	Hash        string           `json:"hash"`
	ContentType string           `json:"content_type"`
	State       string           `json:"state"`
	Metadata    ArtifactMetaJSON `json:"metadata"`
}

type ArtifactMetaJSON struct {
	CreatedAt        string `json:"created_at"`
	CreatedBy        string `json:"created_by"`
	SourceCommitSHA  string `json:"source_commit_sha,omitempty"`
	Description      string `json:"description,omitempty"`
	DerivedByVersion string `json:"derived_by_version,omitempty"`
}

type ArtifactPage struct {
	Items      []ArtifactSummary `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

func Artifacts(reader store.Reader) http.Handler {
	if reader == nil {
		panic("httpapi.Artifacts: reader required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		opts, err := parseArtifactsQuery(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		page, err := reader.List(r.Context(), opts)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list_failed")
			return
		}
		writeJSON(w, http.StatusOK, toArtifactPage(page))
	})
}

func parseArtifactsQuery(r *http.Request) (store.ListOptions, error) {
	opts := store.ListOptions{Cursor: r.URL.Query().Get("cursor")}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return store.ListOptions{}, errors.New("invalid_limit")
		}
		opts.Limit = n
	}
	if raw := r.URL.Query().Get("state"); raw != "" {
		switch store.State(raw) {
		case store.StateStaged, store.StateActive, store.StateDeprecated:
			opts.State = store.State(raw)
		default:
			return store.ListOptions{}, errors.New("invalid_state")
		}
	}
	return opts, nil
}

func toArtifactPage(p store.Page) ArtifactPage {
	out := ArtifactPage{
		Items:      make([]ArtifactSummary, 0, len(p.Items)),
		NextCursor: p.NextCursor,
	}
	for _, s := range p.Items {
		out.Items = append(out.Items, ArtifactSummary{
			Hash:        string(s.Hash),
			ContentType: string(s.ContentType),
			State:       string(s.State),
			Metadata: ArtifactMetaJSON{
				CreatedAt:        s.Metadata.CreatedAt.UTC().Format(time.RFC3339Nano),
				CreatedBy:        s.Metadata.CreatedBy,
				SourceCommitSHA:  s.Metadata.SourceCommitSHA,
				Description:      s.Metadata.Description,
				DerivedByVersion: s.Metadata.DerivedByVersion,
			},
		})
	}
	return out
}
