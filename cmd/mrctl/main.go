// Package main is the mrctl operator CLI entry point. Subcommands
// cover the read surface (ADR-0004: artifacts, artifact, state,
// history, audit) and the write lifecycle (ADR-0005: upload,
// promote, rollback).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	defaultRegistry      = "http://localhost:8090"
	defaultClientTimeout = 30 * time.Second
)

func main() {
	os.Exit(Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, nil))
}

// Run is the testable entrypoint. Returns the process exit code.
// httpClient is the seam tests use to inject httptest.Server; production
// callers pass nil and Run falls back to a default http.Client.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, httpClient *http.Client) int {
	configurePropagator()
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage())
		return 2
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultClientTimeout}
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "artifacts":
		return runArtifacts(ctx, rest, stdout, stderr, httpClient)
	case "artifact":
		return runArtifact(ctx, rest, stdout, stderr, httpClient)
	case "state":
		return runState(ctx, rest, stdout, stderr, httpClient)
	case "history":
		return runHistory(ctx, rest, stdout, stderr, httpClient)
	case "audit":
		return runAudit(ctx, rest, stdout, stderr, httpClient)
	case "upload":
		return runUpload(ctx, rest, stdout, stderr, httpClient)
	case "promote":
		return runPromote(ctx, rest, stdout, stderr, httpClient)
	case "rollback":
		return runRollback(ctx, rest, stdout, stderr, httpClient)
	case "reject":
		return runReject(ctx, rest, stdout, stderr, httpClient)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, usage())
		return 0
	default:
		fmt.Fprintf(stderr, "unknown subcommand %q\n\n%s\n", cmd, usage())
		return 2
	}
}

func usage() string {
	return `mrctl — operator CLI for the model-registry

Read subcommands (ADR-0004):
  artifacts [--limit N] [--state S] [--json]   list artifacts
  artifact <hash> [--json]                     get one bundle
  artifact <hash> <member>                     stream member bytes
                                                  (source|snapshot|diagnose)
  state <env> [--json]                         current env state
  history <env> [--limit N] [--json]           env transition history
  audit [--limit N] [--json]                   operator action log

Write subcommands (ADR-0005, ADR-0009):
  upload --file <path> [--snapshot <path>] [--diagnose <path>]
                       [--operator <o>] [--description <d>] [--json]
  promote --hash <h> --env <e> [--operator <o>] [--reason <r>] [--json]
  rollback --env <e> [--operator <o>] --reason <r> [--json]
  reject --env <e> [--operator <o>] --reason <r> [--json]
                       reject the env's current challenger

Flags:
  --registry URL    registry base URL (default ` + defaultRegistry + `)`
}

// commonFlags bundles --registry across every subcommand.
type commonFlags struct {
	registry string
	jsonOut  bool
}

func registerCommonFlags(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{}
	fs.StringVar(&c.registry, "registry", defaultRegistry, "registry base URL")
	fs.BoolVar(&c.jsonOut, "json", false, "emit JSON instead of TSV")
	return c
}

func parseFlags(fs *flag.FlagSet, args []string, stderr io.Writer) (int, bool) {
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2, false
	}
	return 0, true
}
