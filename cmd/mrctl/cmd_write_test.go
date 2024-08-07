package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/helmedeiros/model-registry/internal/audit/memaudit"
	"github.com/helmedeiros/model-registry/internal/deployer"
	"github.com/helmedeiros/model-registry/internal/envstate/memstate"
	"github.com/helmedeiros/model-registry/internal/httpapi"
	"github.com/helmedeiros/model-registry/internal/instances"
	"github.com/helmedeiros/model-registry/internal/observability/jsonlog"
	"github.com/helmedeiros/model-registry/internal/observability/metrics/prom"
	"github.com/helmedeiros/model-registry/internal/store"
	"github.com/helmedeiros/model-registry/internal/store/memstore"
	"github.com/helmedeiros/model-registry/internal/ulid"
)

// autoOKDeployer is an mrctl-side test deployer that, by default,
// returns OutcomeOK with one StatusDeployed result per target. Named
// to make the auto-fill behaviour explicit so a reviewer cannot
// mistake it for the explicit-shape stub the internal/httpapi tests
// use (those want to assert specific outcomes; these want the happy
// path implicitly so the CLI round-trip stays short).
type autoOKDeployer struct {
	out deployer.DeployResult
	err error
}

func (s autoOKDeployer) Deploy(_ context.Context, targets []instances.Instance, _ deployer.Body) (deployer.DeployResult, error) {
	if s.err != nil {
		return deployer.DeployResult{}, s.err
	}
	out := s.out
	if len(out.Instances) == 0 {
		for _, t := range targets {
			out.Instances = append(out.Instances, deployer.InstanceResult{
				URL: t.URL, Status: deployer.StatusDeployed, Duration: 5 * time.Millisecond,
			})
		}
		out.Outcome = deployer.OutcomeOK
	}
	return out, nil
}

type stubDiscovery struct {
	targets []instances.Instance
	err     error
}

func (s stubDiscovery) Instances(_ context.Context, _ string) ([]instances.Instance, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.targets, nil
}

func startWriteServer(t *testing.T) (*httptest.Server, store.Store) {
	t.Helper()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(tracetest.NewSpanRecorder()))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	logger := jsonlog.New(&bytes.Buffer{}, jsonlog.WithLevel(jsonlog.LevelError))
	st := memstore.New()
	envState := memstate.New()
	au := memaudit.New()
	metrics := prom.New()
	idgen := ulid.New()
	uploadDeps := httpapi.UploadDeps{Substrate: st, Audit: au, ULID: idgen, Logger: logger}
	promoteDeps := httpapi.PromoteDeps{
		Artifacts: st, EnvState: envState, Audit: au,
		Discovery: stubDiscovery{targets: []instances.Instance{{URL: "http://markup-svc-1:8080", Env: "production"}}},
		Deployer:  autoOKDeployer{},
		ULID:      idgen, Logger: logger,
	}
	rollbackDeps := httpapi.RollbackDeps{
		Artifacts: st, EnvState: envState, Audit: au,
		Discovery: promoteDeps.Discovery, Deployer: promoteDeps.Deployer,
		ULID:      idgen, Logger: logger,
	}
	deps := httpapi.Deps{
		AccessLog: logger, Metrics: metrics, PanicSink: logger,
		Tracer:    tp.Tracer("mrctl-write-test"),
		Ready:     func() (string, bool) { return "", true },
		Artifacts: st, EnvState: envState, Audit: au,
		Upload:    &uploadDeps,
		Promote:   &promoteDeps,
		Rollback:  &rollbackDeps,
	}
	server := httptest.NewServer(httpapi.NewRouter(deps, metrics.Handler()))
	t.Cleanup(server.Close)
	return server, st
}

func writeTempCSV(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.csv")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUploadCommandPostsToRegistryAndPrintsHash(t *testing.T) {
	server, _ := startWriteServer(t)
	source := writeTempCSV(t, "alpha,rule,1.0,1\n")

	var stdout bytes.Buffer
	args := []string{
		"upload", "--registry", server.URL,
		"--file", source,
		"--operator", "alice",
		"--description", "first cut",
		"--json",
	}
	if code := Run(context.Background(), args, &stdout, &bytes.Buffer{}, server.Client()); code != 0 {
		t.Fatalf("exit=%d stdout=%s", code, stdout.String())
	}
	var resp httpapi.UploadResponse
	if err := json.NewDecoder(&stdout).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Hash == "" || resp.State != "staged" {
		t.Fatalf("response: %+v", resp)
	}
}

func TestUploadCommandMissingFileFlagExits2(t *testing.T) {
	server, _ := startWriteServer(t)
	args := []string{"upload", "--registry", server.URL}
	var stderr bytes.Buffer
	if code := Run(context.Background(), args, &bytes.Buffer{}, &stderr, server.Client()); code != 2 {
		t.Fatalf("exit=%d want 2", code)
	}
	if !strings.Contains(stderr.String(), "--file") {
		t.Fatalf("stderr missing usage: %s", stderr.String())
	}
}

func TestPromoteCommandPostsAndPrintsOutcome(t *testing.T) {
	server, st := startWriteServer(t)
	h, err := st.Put(context.Background(), store.PutRequest{
		SourceBytes: []byte("alpha,rule,1.0,1\n"),
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedBy: "ci-bot"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	args := []string{
		"promote", "--registry", server.URL,
		"--hash", string(h),
		"--env", "production",
		"--operator", "alice",
		"--reason", "weekly",
		"--json",
	}
	if code := Run(context.Background(), args, &stdout, &bytes.Buffer{}, server.Client()); code != 0 {
		t.Fatalf("exit=%d body=%s", code, stdout.String())
	}
	var resp httpapi.PromoteResponse
	if err := json.NewDecoder(&stdout).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.NewHash != string(h) || resp.Env != "production" || resp.Deploy.Outcome != "ok" {
		t.Fatalf("response: %+v", resp)
	}
}

func TestPromoteCommandMissingHashExits2(t *testing.T) {
	server, _ := startWriteServer(t)
	args := []string{"promote", "--registry", server.URL, "--env", "production"}
	var stderr bytes.Buffer
	if code := Run(context.Background(), args, &bytes.Buffer{}, &stderr, server.Client()); code != 2 {
		t.Fatalf("exit=%d want 2", code)
	}
}

func TestRollbackCommandPostsAndPrintsOutcome(t *testing.T) {
	server, st := startWriteServer(t)
	h1, err := st.Put(context.Background(), store.PutRequest{
		SourceBytes: []byte("alpha,rule,1.0,1\n"),
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedBy: "ci-bot"},
	})
	if err != nil {
		t.Fatal(err)
	}
	h2, err := st.Put(context.Background(), store.PutRequest{
		SourceBytes: []byte("beta,rule,1.0,1\n"),
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedBy: "ci-bot"},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, h := range []store.Hash{h1, h2} {
		args := []string{
			"promote", "--registry", server.URL,
			"--hash", string(h),
			"--env", "production",
			"--operator", "ci-bot",
			"--json",
		}
		if code := Run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}, server.Client()); code != 0 {
			t.Fatalf("promote %s: exit=%d", h, code)
		}
	}

	var stdout bytes.Buffer
	args := []string{
		"rollback", "--registry", server.URL,
		"--env", "production",
		"--operator", "alice",
		"--reason", "h2 misbehaved",
		"--json",
	}
	if code := Run(context.Background(), args, &stdout, &bytes.Buffer{}, server.Client()); code != 0 {
		t.Fatalf("exit=%d body=%s", code, stdout.String())
	}
	var resp httpapi.RollbackResponse
	if err := json.NewDecoder(&stdout).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.RolledTo != string(h1) {
		t.Fatalf("rolled_to=%q want %q", resp.RolledTo, h1)
	}
}

func TestRollbackCommandMissingReasonExits2(t *testing.T) {
	server, _ := startWriteServer(t)
	args := []string{"rollback", "--registry", server.URL, "--env", "production"}
	var stderr bytes.Buffer
	if code := Run(context.Background(), args, &bytes.Buffer{}, &stderr, server.Client()); code != 2 {
		t.Fatalf("exit=%d want 2", code)
	}
}

func TestUsageMentionsWriteSubcommands(t *testing.T) {
	for _, kw := range []string{"upload", "promote", "rollback"} {
		if !strings.Contains(usage(), kw) {
			t.Fatalf("usage missing %q", kw)
		}
	}
	if !strings.Contains(usage(), "ADR-0005") {
		t.Fatal("usage missing ADR-0005 mention")
	}
}

// Ensure DefaultOperator works even when user.Current fails — it
// falls back to $USER. Skip if neither resolver returns anything.
func TestDefaultOperatorReturnsSomething(t *testing.T) {
	op := defaultOperator()
	if op == "" {
		t.Skip("running in an environment without user.Current and USER set")
	}
}

var _ http.Handler = (http.Handler)(nil) // keep net/http import live
