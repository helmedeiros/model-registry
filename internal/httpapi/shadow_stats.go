package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/helmedeiros/model-registry/internal/shadowstats"
)

type ShadowStatsDeps struct {
	Reader shadowstats.Reader
}

func ShadowStats(deps ShadowStatsDeps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		since, err := parseSince(r.URL.Query().Get("since"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_since")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		defer cancel()

		stats, err := deps.Reader.Stats(ctx, since)
		if err != nil {
			if errors.Is(err, shadowstats.ErrDisabled) {
				writeError(w, http.StatusServiceUnavailable, "shadow_stats_disabled")
				return
			}
			writeError(w, http.StatusBadGateway, "shadow_stats_upstream")
			return
		}
		writeJSON(w, http.StatusOK, toShadowStatsView(stats))
	})
}

func toShadowStatsView(s shadowstats.Stats) ShadowStatsView {
	return ShadowStatsView{
		Since:                s.Since.String(),
		AgreementRate:        s.AgreementRate,
		AgreementSamples:     s.AgreementSamples,
		OneSidedChampionRPS:  s.OneSidedChampionRPS,
		OneSidedChallengerRPS: s.OneSidedChallengerRPS,
		TimeoutRPS:           s.TimeoutRPS,
		ErrorRPS:             s.ErrorRPS,
		FactorDeltaP50:       s.FactorDeltaP50,
		FactorDeltaP95:       s.FactorDeltaP95,
		FactorDeltaP99:       s.FactorDeltaP99,
	}
}
