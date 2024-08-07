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
		fmt.Fprintln(stderr, err)
		return 1
	}

	if common.jsonOut {
		return emitJSON(stdout, stderr, resp)
	}
	printDeployResult(stdout, resp.Env, resp.PreviousHash, resp.NewHash, resp.Deploy)
	return 0
}
