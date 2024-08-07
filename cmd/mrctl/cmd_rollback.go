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

func runRollback(ctx context.Context, args []string, stdout, stderr io.Writer, c *http.Client) int {
	fs := flag.NewFlagSet("rollback", flag.ContinueOnError)
	common := registerCommonFlags(fs)
	env := fs.String("env", "", "target env (required)")
	operator := fs.String("operator", defaultOperator(), "operator identity for the audit trail")
	reason := fs.String("reason", "", "reason for the rollback (required)")
	if code, ok := parseFlags(fs, args, stderr); !ok {
		return code
	}
	if *env == "" || *reason == "" {
		fmt.Fprintln(stderr, "usage: mrctl rollback --env <e> [--operator <o>] --reason <r>")
		return 2
	}

	body, err := json.Marshal(httpapi.RollbackRequest{
		Env: *env, Operator: *operator, Reason: *reason,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	var resp httpapi.RollbackResponse
	if _, err := postJSON(ctx, c, common.registry, "/rollback", bytes.NewReader(body), "application/json", &resp); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if common.jsonOut {
		return emitJSON(stdout, stderr, resp)
	}
	printDeployResult(stdout, resp.Env, resp.PreviousHash, resp.RolledTo, resp.Deploy)
	return 0
}

func printDeployResult(out io.Writer, env, previousHash, newHash string, deploy httpapi.DeployView) {
	fmt.Fprintf(out, "env:          %s\n", env)
	if previousHash != "" {
		fmt.Fprintf(out, "previous:     %s\n", previousHash)
	}
	fmt.Fprintf(out, "now_champion: %s\n", newHash)
	fmt.Fprintf(out, "deploy:       %s\n", deploy.Outcome)
	for _, ir := range deploy.Instances {
		marker := "✔"
		if ir.Status != "deployed" {
			marker = "✗"
		}
		fmt.Fprintf(out, "  %s %s (%s in %d ms)", marker, ir.URL, ir.Status, ir.DurationMS)
		if ir.Error != "" {
			fmt.Fprintf(out, "  %s", ir.Error)
		}
		fmt.Fprintln(out)
	}
}
