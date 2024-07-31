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

func runAudit(ctx context.Context, args []string, stdout, stderr io.Writer, c *http.Client) int {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	common := registerCommonFlags(fs)
	limit := fs.Int("limit", 0, "page size; 0 = registry default")
	if code, ok := parseFlags(fs, args, stderr); !ok {
		return code
	}

	q := url.Values{}
	if *limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", *limit))
	}

	var page httpapi.AuditPage
	if _, err := doJSON(ctx, c, common.registry, "/audit", q, &page); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if common.jsonOut {
		return emitJSON(stdout, stderr, page)
	}
	fmt.Fprintf(stdout, "%-30s\t%-10s\t%-12s\t%s\n", "AT", "OPERATOR", "ACTION", "TARGET")
	for _, e := range page.Items {
		fmt.Fprintf(stdout, "%-30s\t%-10s\t%-12s\t%s\n", e.At, e.Operator, e.Action, e.Target)
	}
	if page.NextCursor != "" {
		fmt.Fprintf(stdout, "\nnext_cursor: %s\n", page.NextCursor)
	}
	return 0
}
