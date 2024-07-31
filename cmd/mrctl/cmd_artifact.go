package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"

	"github.com/helmedeiros/model-registry/internal/httpapi"
)

func runArtifact(ctx context.Context, args []string, stdout, stderr io.Writer, c *http.Client) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: mrctl artifact <hash> [member] [--json]")
		return 2
	}
	hash := args[0]
	rest := args[1:]

	var member string
	if len(rest) > 0 && !startsWithDash(rest[0]) {
		member = rest[0]
		rest = rest[1:]
	}

	fs := flag.NewFlagSet("artifact", flag.ContinueOnError)
	common := registerCommonFlags(fs)
	if code, ok := parseFlags(fs, rest, stderr); !ok {
		return code
	}

	if member != "" {
		path := "/artifact/" + hash + "/" + member
		if _, err := doStream(ctx, c, common.registry, path, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}

	var bundle httpapi.ArtifactBundle
	if _, err := doJSON(ctx, c, common.registry, "/artifact/"+hash, nil, &bundle); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if common.jsonOut {
		return emitJSON(stdout, stderr, bundle)
	}
	fmt.Fprintf(stdout, "hash:           %s\n", bundle.Hash)
	fmt.Fprintf(stdout, "state:          %s\n", bundle.State)
	fmt.Fprintf(stdout, "content_type:   %s\n", bundle.ContentType)
	fmt.Fprintf(stdout, "created_at:     %s\n", bundle.Metadata.CreatedAt)
	fmt.Fprintf(stdout, "created_by:     %s\n", bundle.Metadata.CreatedBy)
	if bundle.Metadata.Description != "" {
		fmt.Fprintf(stdout, "description:    %s\n", bundle.Metadata.Description)
	}
	fmt.Fprintf(stdout, "has_snapshot:   %v\n", bundle.HasSnapshot)
	fmt.Fprintf(stdout, "has_diagnose:   %v\n", bundle.HasDiagnose)
	return 0
}

func startsWithDash(s string) bool { return len(s) > 0 && s[0] == '-' }
