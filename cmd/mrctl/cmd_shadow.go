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

// Promote-to-champion gate thresholds (ADR-0013). --gate enforces all
// three; --min-agreement / --min-samples / --max-factor-delta-p99
// override individual thresholds.
const (
	defaultMinAgreement       = 0.99
	defaultMinSamples         = 10000
	defaultMaxFactorDeltaP99  = 0.05
)

func runShadow(ctx context.Context, args []string, stdout, stderr io.Writer, c *http.Client) int {
	fs := flag.NewFlagSet("shadow", flag.ContinueOnError)
	common := registerCommonFlags(fs)
	since := fs.String("since", "", "lookback window (e.g. 5m, 1h)")
	gate := fs.Bool("gate", false, "exit non-zero unless agreement >= --min-agreement AND samples >= --min-samples AND factor_delta_p99 <= --max-factor-delta-p99 (ADR-0013 promote-to-champion gate)")
	minAgreement := fs.Float64("min-agreement", defaultMinAgreement, "minimum agreement rate for --gate")
	minSamples := fs.Float64("min-samples", defaultMinSamples, "minimum sample count for --gate")
	maxFactorDeltaP99 := fs.Float64("max-factor-delta-p99", defaultMaxFactorDeltaP99, "maximum factor_delta_p99 for --gate")
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
	if !*gate {
		return 0
	}
	return evaluateGate(stdout, stderr, view, *minAgreement, *minSamples, *maxFactorDeltaP99)
}

func evaluateGate(stdout, stderr io.Writer, view httpapi.ShadowStatsView, minAgreement, minSamples, maxFactorDeltaP99 float64) int {
	var failures []string
	if view.AgreementRate < minAgreement {
		failures = append(failures, fmt.Sprintf("agreement %.4f < %.4f", view.AgreementRate, minAgreement))
	}
	if view.AgreementSamples < minSamples {
		failures = append(failures, fmt.Sprintf("samples %.0f < %.0f", view.AgreementSamples, minSamples))
	}
	if view.FactorDeltaP99 > maxFactorDeltaP99 {
		failures = append(failures, fmt.Sprintf("factor_delta_p99 %.4f > %.4f", view.FactorDeltaP99, maxFactorDeltaP99))
	}
	fmt.Fprintln(stdout)
	if len(failures) == 0 {
		fmt.Fprintln(stdout, "gate: PASS")
		return 0
	}
	fmt.Fprintln(stdout, "gate: FAIL")
	for _, f := range failures {
		fmt.Fprintln(stderr, "  - "+f)
	}
	return 3
}
