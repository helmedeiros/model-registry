package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"

	"github.com/helmedeiros/model-registry/internal/httpapi"
)

func runReject(ctx context.Context, args []string, stdout, stderr io.Writer, c *http.Client) int {
	fs := flag.NewFlagSet("reject", flag.ContinueOnError)
	common := registerCommonFlags(fs)
	env := fs.String("env", "", "target env (required)")
	operator := fs.String("operator", defaultOperator(), "operator identity for the audit trail")
	reason := fs.String("reason", "", "reason for rejecting the challenger (required)")
	if code, ok := parseFlags(fs, args, stderr); !ok {
		return code
	}
	if *env == "" || *reason == "" {
		fmt.Fprintln(stderr, "usage: mrctl reject --env <e> [--operator <o>] --reason <r>")
		return 2
	}

	body, err := json.Marshal(httpapi.RejectRequest{Env: *env, Operator: *operator, Reason: *reason})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	var resp httpapi.RejectResponse
	if _, err := postJSON(ctx, c, common.registry, "/reject", bytes.NewReader(body), "application/json", &resp); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if common.jsonOut {
		return emitJSON(stdout, stderr, resp)
	}
	fmt.Fprintf(stdout, "env:            %s\n", resp.Env)
	if resp.RejectedHash != "" {
		fmt.Fprintf(stdout, "rejected_hash:  %s\n", resp.RejectedHash)
	}
	return 0
}
