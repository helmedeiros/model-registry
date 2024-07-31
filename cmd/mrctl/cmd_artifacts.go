package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/helmedeiros/model-registry/internal/httpapi"
)

func runArtifacts(ctx context.Context, args []string, stdout, stderr io.Writer, c *http.Client) int {
	fs := flag.NewFlagSet("artifacts", flag.ContinueOnError)
	common := registerCommonFlags(fs)
	limit := fs.Int("limit", 0, "page size; 0 = registry default")
	state := fs.String("state", "", "state filter: staged|active|deprecated")
	if code, ok := parseFlags(fs, args, stderr); !ok {
		return code
	}

	q := url.Values{}
	if *limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", *limit))
	}
	if *state != "" {
		q.Set("state", *state)
	}

	var page httpapi.ArtifactPage
	if _, err := doJSON(ctx, c, common.registry, "/artifacts", q, &page); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if common.jsonOut {
		return emitJSON(stdout, stderr, page)
	}
	fmt.Fprintf(stdout, "%-64s\t%-10s\t%-12s\t%s\n", "HASH", "STATE", "CONTENT_TYPE", "CREATED_BY")
	for _, s := range page.Items {
		fmt.Fprintf(stdout, "%-64s\t%-10s\t%-12s\t%s\n", s.Hash, s.State, s.ContentType, s.Metadata.CreatedBy)
	}
	if page.NextCursor != "" {
		fmt.Fprintf(stdout, "\nnext_cursor: %s\n", page.NextCursor)
	}
	return 0
}

func emitJSON(stdout, stderr io.Writer, v any) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
