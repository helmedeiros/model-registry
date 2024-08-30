package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/helmedeiros/model-registry/internal/httpapi"
)

// runShadow renders the markup-svc shadow-Decider comparison metrics
// (ADR-0013). The promote-to-champion gate the team documents is
// agreement >= 99% AND samples >= 10k AND factor_delta_p99 <= 0.05;
// this command does not enforce the gate, it only surfaces the
// numbers the operator reads against it.
func runShadow(ctx context.Context, args []string, stdout, stderr io.Writer, c *http.Client) int {
	fs := flag.NewFlagSet("shadow", flag.ContinueOnError)
	common := registerCommonFlags(fs)
	since := fs.String("since", "", "lookback window (e.g. 5m, 1h)")
	if code, ok := parseFlags(fs, args, stderr); !ok {
		return code
	}
	q := url.Values{}
	if *since != "" {
		q.Set("since", *since)
	}
	var view httpapi.ShadowStatsView
	if _, err := doJSON(ctx, c, common.registry, "/shadow-stats", q, &view); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if common.jsonOut {
		return emitJSON(stdout, stderr, view)
	}
	fmt.Fprintf(stdout, "since:              %s\n", view.Since)
	fmt.Fprintf(stdout, "effective sample:   %.4f (1.0=every challenged /decide; cross-reference agreement)\n", view.EffectiveSampleRate)
	fmt.Fprintf(stdout, "agreement:          %.4f over %.0f samples\n", view.AgreementRate, view.AgreementSamples)
	fmt.Fprintf(stdout, "one-sided:          champion=%.4f rps  challenger=%.4f rps\n", view.OneSidedChampionRPS, view.OneSidedChallengerRPS)
	fmt.Fprintf(stdout, "eval:               timeout=%.4f rps  error=%.4f rps\n", view.TimeoutRPS, view.ErrorRPS)
	fmt.Fprintf(stdout, "factor delta:       p50=%.4f  p95=%.4f  p99=%.4f\n", view.FactorDeltaP50, view.FactorDeltaP95, view.FactorDeltaP99)
	fmt.Fprintf(stdout, "challenger latency: p50=%.6fs  p95=%.6fs  p99=%.6fs\n", view.ChallengerLatencyP50, view.ChallengerLatencyP95, view.ChallengerLatencyP99)
	return 0
}
