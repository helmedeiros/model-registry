package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/helmedeiros/model-registry/internal/businessstats"
)

const (
	defaultBusinessStatsWindow = 5 * time.Minute
	maxBusinessStatsWindow     = 24 * time.Hour
	minBusinessStatsWindow     = 1 * time.Minute
)

type BusinessStatsDeps struct {
	Reader businessstats.Reader
}

func BusinessStats(deps BusinessStatsDeps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		env := r.PathValue("env")
		if env == "" {
			writeError(w, http.StatusBadRequest, "env_required")
			return
		}
		since, err := parseSince(r.URL.Query().Get("since"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_since")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		defer cancel()

		stats, err := deps.Reader.Stats(ctx, env, since)
		if err != nil {
			if errors.Is(err, businessstats.ErrDisabled) {
				writeError(w, http.StatusServiceUnavailable, "business_stats_disabled")
				return
			}
			writeError(w, http.StatusBadGateway, "business_stats_upstream")
			return
		}
		writeJSON(w, http.StatusOK, toBusinessStatsView(stats))
	})
}

func parseSince(raw string) (time.Duration, error) {
	if raw == "" {
		return defaultBusinessStatsWindow, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, err
	}
	if d < minBusinessStatsWindow {
		return 0, errors.New("must be >= 1m")
	}
	if d > maxBusinessStatsWindow {
		return 0, errors.New("must be <= 24h")
	}
	return d, nil
}

func toBusinessStatsView(s businessstats.Stats) BusinessStatsView {
	rules := make([]BusinessRuleView, 0, len(s.TopRules))
	for _, r := range s.TopRules {
		rules = append(rules, BusinessRuleView{Rule: r.Rule, RatePerSecond: r.RatePerSecond})
	}
	return BusinessStatsView{
		Env:      s.Env,
		Since:    s.Since.String(),
		Decide:   BusinessDecideView{OK: s.DecideRPS.OK, Error: s.DecideRPS.Error, NoMatch: s.DecideRPS.NoMatch, Total: s.DecideRPS.Total},
		Factor:   BusinessFactorView{P50: s.FactorP50, P95: s.FactorP95, P99: s.FactorP99},
		TopRules: rules,
	}
}
