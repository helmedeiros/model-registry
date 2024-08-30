package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"

	"github.com/helmedeiros/model-registry/internal/httpapi"
)

func runDiff(ctx context.Context, args []string, stdout, stderr io.Writer, c *http.Client) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: mrctl diff <from-hash> <to-hash> [--json]")
		return 2
	}
	from, to := args[0], args[1]
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	common := registerCommonFlags(fs)
	if code, ok := parseFlags(fs, args[2:], stderr); !ok {
		return code
	}

	var view httpapi.ArtifactDiffView
	if _, err := doJSON(ctx, c, common.registry, "/artifact/"+from+"/diff/"+to, nil, &view); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if common.jsonOut {
		return emitJSON(stdout, stderr, view)
	}
	fmt.Fprintf(stdout, "from: %s\n", view.From)
	fmt.Fprintf(stdout, "to:   %s\n", view.To)
	renderRules(stdout, "added", view.Added)
	renderRules(stdout, "removed", view.Removed)
	if len(view.Modified) > 0 {
		fmt.Fprintln(stdout, "modified:")
		for _, m := range view.Modified {
			fmt.Fprintf(stdout, "  %s\n", m.To.RuleID)
			fmt.Fprintf(stdout, "    from: %s by %s\n", short(m.From.SourceCommitSHA), m.From.Author)
			fmt.Fprintf(stdout, "    to:   %s by %s\n", short(m.To.SourceCommitSHA), m.To.Author)
		}
	}
	return 0
}

func renderRules(w io.Writer, label string, rs []httpapi.RuleProvenanceJSON) {
	if len(rs) == 0 {
		return
	}
	fmt.Fprintf(w, "%s:\n", label)
	for _, r := range rs {
		fmt.Fprintf(w, "  %-30s %s by %s\n", r.RuleID, short(r.SourceCommitSHA), r.Author)
	}
}

func short(sha string) string {
	if len(sha) < 8 {
		return sha
	}
	return sha[:8]
}
