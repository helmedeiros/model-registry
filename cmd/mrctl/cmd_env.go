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

func runState(ctx context.Context, args []string, stdout, stderr io.Writer, c *http.Client) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: mrctl state <env> [--json]")
		return 2
	}
	env := args[0]
	fs := flag.NewFlagSet("state", flag.ContinueOnError)
	common := registerCommonFlags(fs)
	if code, ok := parseFlags(fs, args[1:], stderr); !ok {
		return code
	}

	var state httpapi.EnvStateView
	if _, err := doJSON(ctx, c, common.registry, "/env/"+env+"/state", nil, &state); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if common.jsonOut {
		return emitJSON(stdout, stderr, state)
	}
	fmt.Fprintf(stdout, "env:        %s\n", state.Env)
	fmt.Fprintf(stdout, "champion:   %s\n", roleSummary(state.Champion))
	fmt.Fprintf(stdout, "challenger: %s\n", roleSummary(state.Challenger))
	if state.UpdatedAt != nil {
		fmt.Fprintf(stdout, "updated_at: %s\n", *state.UpdatedAt)
	}
	return 0
}

func runHistory(ctx context.Context, args []string, stdout, stderr io.Writer, c *http.Client) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: mrctl history <env> [--limit N] [--json]")
		return 2
	}
	env := args[0]
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	common := registerCommonFlags(fs)
	limit := fs.Int("limit", 0, "page size; 0 = registry default")
	if code, ok := parseFlags(fs, args[1:], stderr); !ok {
		return code
	}

	q := url.Values{}
	if *limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", *limit))
	}

	var page httpapi.EnvHistoryPage
	if _, err := doJSON(ctx, c, common.registry, "/env/"+env+"/history", q, &page); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if common.jsonOut {
		return emitJSON(stdout, stderr, page)
	}
	fmt.Fprintf(stdout, "%-30s\t%-22s\t%-10s\t%s\n", "AT", "KIND", "OPERATOR", "TO_HASH")
	for _, t := range page.Items {
		fmt.Fprintf(stdout, "%-30s\t%-22s\t%-10s\t%s\n", t.At, t.Kind, t.Operator, t.ToHash)
	}
	if page.NextCursor != "" {
		fmt.Fprintf(stdout, "\nnext_cursor: %s\n", page.NextCursor)
	}
	return 0
}

func roleSummary(r *httpapi.EnvRoleView) string {
	if r == nil {
		return "(none)"
	}
	return fmt.Sprintf("%s (by %s at %s)", r.Hash, r.PromotedBy, r.PromotedAt)
}
