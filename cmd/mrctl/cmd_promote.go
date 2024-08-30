package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"

	"github.com/helmedeiros/model-registry/internal/httpapi"
)

func runPromote(ctx context.Context, args []string, stdout, stderr io.Writer, c *http.Client) int {
	fs := flag.NewFlagSet("promote", flag.ContinueOnError)
	common := registerCommonFlags(fs)
	hash := fs.String("hash", "", "artifact hash to promote (required)")
	env := fs.String("env", "", "target env (required)")
	operator := fs.String("operator", defaultOperator(), "operator identity for the audit trail")
	reason := fs.String("reason", "", "optional reason recorded with the audit entry")
	role := fs.String("role", "champion", "promote role: champion|challenger (v0.0.4 ships champion)")
	if code, ok := parseFlags(fs, args, stderr); !ok {
		return code
	}
	if *hash == "" || *env == "" {
		fmt.Fprintln(stderr, "usage: mrctl promote --hash <h> --env <e> [--operator <o>] [--reason <r>]")
		return 2
	}

	body, err := json.Marshal(httpapi.PromoteRequest{
		Hash: *hash, Env: *env, Role: *role, Operator: *operator, Reason: *reason,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	var resp httpapi.PromoteResponse
	if _, err := postJSON(ctx, c, common.registry, "/promote", bytes.NewReader(body), "application/json", &resp); err != nil {
		var he *httpError
		if errors.As(err, &he) {
			switch he.status {
			case http.StatusUnprocessableEntity:
				return renderPromoteRejection(stdout, stderr, common.jsonOut, he.body)
			case http.StatusTooManyRequests:
				return renderRateLimited(stderr, "promote", he)
			}
		}
		fmt.Fprintln(stderr, err)
		return 1
	}

	if common.jsonOut {
		return emitJSON(stdout, stderr, resp)
	}
	printDeployResult(stdout, resp.Env, resp.PreviousHash, resp.NewHash, resp.Deploy)
	return 0
}

func renderPromoteRejection(stdout, stderr io.Writer, jsonOut bool, body []byte) int {
	var rej httpapi.PromoteRejectedResponse
	if err := json.Unmarshal(body, &rej); err != nil {
		fmt.Fprintln(stderr, "registry: 422:", string(body))
		return 1
	}
	if jsonOut {
		_ = emitJSON(stdout, stderr, rej)
		return 1
	}
	fmt.Fprintf(stderr, "promote_rejected: %s (hash=%s env=%s)\n", rej.Reason, rej.NewHash, rej.Env)
	if rej.Diagnose != nil {
		for _, e := range rej.Diagnose.Errors {
			fmt.Fprintf(stderr, "  ✗ %s [%s]: %s\n", e.Kind, e.Rule, e.Detail)
		}
		for _, w := range rej.Diagnose.Warnings {
			fmt.Fprintf(stderr, "  ⚠ %s [%s]: %s\n", w.Kind, w.Rule, w.Detail)
		}
	}
	return 1
}
