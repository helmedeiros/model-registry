package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/helmedeiros/model-registry/internal/envstate"
)

// EnvState serves GET /env/{env}/state. An env that has never been
// touched returns the empty envelope rather than 404 so dashboards
// can scrape any env safely.
func EnvState(reader envstate.Reader) http.Handler {
	if reader == nil {
		panic("httpapi.EnvState: reader required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		env := r.PathValue("env")
		if env == "" {
			writeError(w, http.StatusBadRequest, "missing_env")
			return
		}
		state, err := reader.Get(r.Context(), env)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "get_failed")
			return
		}
		writeJSON(w, http.StatusOK, toEnvStateView(state))
	})
}

// EnvHistory serves GET /env/{env}/history.
func EnvHistory(reader envstate.Reader) http.Handler {
	if reader == nil {
		panic("httpapi.EnvHistory: reader required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		env := r.PathValue("env")
		if env == "" {
			writeError(w, http.StatusBadRequest, "missing_env")
			return
		}
		opts, err := parseEnvHistoryQuery(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		page, err := reader.History(r.Context(), env, opts)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "history_failed")
			return
		}
		writeJSON(w, http.StatusOK, toEnvHistoryPage(page))
	})
}

func parseEnvHistoryQuery(r *http.Request) (envstate.ListOptions, error) {
	opts := envstate.ListOptions{Cursor: r.URL.Query().Get("cursor")}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return envstate.ListOptions{}, errors.New("invalid_limit")
		}
		if n > envstate.MaxListLimit {
			return envstate.ListOptions{}, errors.New("limit_too_large")
		}
		opts.Limit = n
	}
	return opts, nil
}

func toEnvStateView(s envstate.State) EnvStateView {
	view := EnvStateView{
		Env:        s.Env,
		Champion:   toRoleView(s.Champion),
		Challenger: toRoleView(s.Challenger),
	}
	if !s.UpdatedAt.IsZero() {
		formatted := s.UpdatedAt.UTC().Format(time.RFC3339Nano)
		view.UpdatedAt = &formatted
	}
	return view
}

func toRoleView(r *envstate.Role) *EnvRoleView {
	if r == nil {
		return nil
	}
	return &EnvRoleView{
		Hash:       string(r.Hash),
		PromotedBy: r.PromotedBy,
		PromotedAt: r.PromotedAt.UTC().Format(time.RFC3339Nano),
	}
}

func toEnvHistoryPage(p envstate.HistoryPage) EnvHistoryPage {
	out := EnvHistoryPage{
		Items:      make([]EnvTransitionView, 0, len(p.Items)),
		NextCursor: p.NextCursor,
	}
	for _, t := range p.Items {
		out.Items = append(out.Items, EnvTransitionView{
			Env:      t.Env,
			Kind:     string(t.Kind),
			FromHash: string(t.FromHash),
			ToHash:   string(t.ToHash),
			Operator: t.Operator,
			Reason:   t.Reason,
			At:       t.At.UTC().Format(time.RFC3339Nano),
		})
	}
	return out
}
