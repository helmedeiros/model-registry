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

func runStats(ctx context.Context, args []string, stdout, stderr io.Writer, c *http.Client) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: mrctl stats <env> [--since 5m] [--json]")
		return 2
	}
	env := args[0]
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	common := registerCommonFlags(fs)
	since := fs.String("since", "", "lookback window (e.g. 5m, 1h)")
	if code, ok := parseFlags(fs, args[1:], stderr); !ok {
		return code
	}

	q := url.Values{}
	if *since != "" {
		q.Set("since", *since)
	}

	var view httpapi.BusinessStatsView
	if _, err := doJSON(ctx, c, common.registry, "/env/"+env+"/business-stats", q, &view); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if common.jsonOut {
		return emitJSON(stdout, stderr, view)
	}
	fmt.Fprintf(stdout, "env:    %s\n", view.Env)
	fmt.Fprintf(stdout, "since:  %s\n", view.Since)
	fmt.Fprintf(stdout, "decide: total=%.3f rps  ok=%.3f  err=%.3f  no_match=%.3f\n",
		view.Decide.Total, view.Decide.OK, view.Decide.Error, view.Decide.NoMatch)
	fmt.Fprintf(stdout, "factor: p50=%.4f  p95=%.4f  p99=%.4f\n", view.Factor.P50, view.Factor.P95, view.Factor.P99)
	if len(view.TopRules) > 0 {
		fmt.Fprintln(stdout, "top_rules:")
		for _, r := range view.TopRules {
			fmt.Fprintf(stdout, "  %-30s %.3f rps\n", r.Rule, r.RatePerSecond)
		}
	}
	return 0
}
