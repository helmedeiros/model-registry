package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/helmedeiros/model-registry/internal/audit"
)

// Audit serves GET /audit. Reverse-chronological list of operator
// actions across all envs.
func Audit(reader audit.Reader) http.Handler {
	if reader == nil {
		panic("httpapi.Audit: reader required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		opts, err := parseAuditQuery(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		page, err := reader.List(r.Context(), opts)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list_failed")
			return
		}
		writeJSON(w, http.StatusOK, toAuditPage(page))
	})
}

func parseAuditQuery(r *http.Request) (audit.ListOptions, error) {
	q := r.URL.Query()
	opts := audit.ListOptions{Cursor: q.Get("cursor")}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return audit.ListOptions{}, errors.New("invalid_limit")
		}
		if n > audit.MaxListLimit {
			return audit.ListOptions{}, errors.New("limit_too_large")
		}
		opts.Limit = n
	}
	return opts, nil
}

func toAuditPage(p audit.Page) AuditPage {
	out := AuditPage{
		Items:      make([]AuditEntryView, 0, len(p.Items)),
		NextCursor: p.NextCursor,
	}
	for _, e := range p.Items {
		out.Items = append(out.Items, AuditEntryView{
			ID:           e.ID,
			Operator:     e.Operator,
			Action:       e.Action,
			Target:       e.Target,
			ArtifactHash: string(e.ArtifactHash),
			Reason:       e.Reason,
			At:           e.At.UTC().Format(time.RFC3339Nano),
		})
	}
	return out
}
