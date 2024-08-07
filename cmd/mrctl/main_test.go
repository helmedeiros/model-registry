package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/helmedeiros/model-registry/internal/audit/memaudit"
	"github.com/helmedeiros/model-registry/internal/envstate/memstate"
	"github.com/helmedeiros/model-registry/internal/httpapi"
	"github.com/helmedeiros/model-registry/internal/observability/jsonlog"
	"github.com/helmedeiros/model-registry/internal/observability/metrics/prom"
	"github.com/helmedeiros/model-registry/internal/store/memstore"
)

func TestRun_NoArgsPrintsUsageAndExits2(t *testing.T) {
	var stderr bytes.Buffer
	if code := Run(context.Background(), nil, nil, &stderr, nil); code != 2 {
		t.Fatalf("exit=%d want 2", code)
	}
	if !strings.Contains(stderr.String(), "mrctl") {
		t.Fatalf("usage missing from stderr: %s", stderr.String())
	}
}

func TestRun_HelpExitsZero(t *testing.T) {
	for _, flag := range []string{"-h", "--help", "help"} {
		var stdout bytes.Buffer
		if code := Run(context.Background(), []string{flag}, &stdout, nil, nil); code != 0 {
			t.Fatalf("%s exit=%d want 0", flag, code)
		}
		if !strings.Contains(stdout.String(), "subcommands") {
			t.Fatalf("%s output missing usage: %s", flag, stdout.String())
		}
	}
}

func TestRun_UnknownSubcommandExits2(t *testing.T) {
	var stderr bytes.Buffer
	if code := Run(context.Background(), []string{"banana"}, nil, &stderr, nil); code != 2 {
		t.Fatalf("exit=%d want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown subcommand") {
		t.Fatalf("missing error message: %s", stderr.String())
	}
}

func TestRun_ArtifactsTalksToServer(t *testing.T) {
	ts, _ := startServer(t)
	defer ts.Close()

	var stdout bytes.Buffer
	args := []string{"artifacts", "--registry", ts.URL, "--json"}
	if code := Run(context.Background(), args, &stdout, nil, ts.Client()); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	var page httpapi.ArtifactPage
	if err := json.NewDecoder(&stdout).Decode(&page); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout.String())
	}
	if page.Items == nil {
		t.Fatal("items should be initialised, even if empty")
	}
}

func TestRun_StateTalksToServer(t *testing.T) {
	ts, _ := startServer(t)
	defer ts.Close()

	var stdout bytes.Buffer
	args := []string{"state", "production", "--registry", ts.URL, "--json"}
	if code := Run(context.Background(), args, &stdout, nil, ts.Client()); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	var state httpapi.EnvStateView
	if err := json.NewDecoder(&stdout).Decode(&state); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout.String())
	}
	if state.Env != "production" {
		t.Fatalf("env=%q want production", state.Env)
	}
	if state.Champion != nil || state.Challenger != nil {
		t.Fatalf("fresh env should be empty: %+v", state)
	}
}

func TestRun_HistoryTalksToServer(t *testing.T) {
	ts, _ := startServer(t)
	defer ts.Close()

	var stdout bytes.Buffer
	args := []string{"history", "production", "--registry", ts.URL, "--json"}
	if code := Run(context.Background(), args, &stdout, nil, ts.Client()); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	var page httpapi.EnvHistoryPage
	if err := json.NewDecoder(&stdout).Decode(&page); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout.String())
	}
	if len(page.Items) != 0 {
		t.Fatalf("fresh env should have no history; got %d", len(page.Items))
	}
}

func TestRun_AuditTalksToServer(t *testing.T) {
	ts, _ := startServer(t)
	defer ts.Close()

	var stdout bytes.Buffer
	args := []string{"audit", "--registry", ts.URL, "--json"}
	if code := Run(context.Background(), args, &stdout, nil, ts.Client()); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	var page httpapi.AuditPage
	if err := json.NewDecoder(&stdout).Decode(&page); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout.String())
	}
	if len(page.Items) != 0 {
		t.Fatalf("fresh audit should be empty; got %d", len(page.Items))
	}
}

func TestRun_ArtifactUnknownHashExitsNonZero(t *testing.T) {
	ts, _ := startServer(t)
	defer ts.Close()

	var stderr bytes.Buffer
	args := []string{"artifact", "unknown", "--registry", ts.URL}
	if code := Run(context.Background(), args, nil, &stderr, ts.Client()); code != 1 {
		t.Fatalf("exit=%d want 1", code)
	}
	if !strings.Contains(stderr.String(), "not_found") {
		t.Fatalf("stderr missing not_found: %s", stderr.String())
	}
}

func TestRun_OutboundCarriesTraceParent(t *testing.T) {
	// Stand up a registry with a recording tracer; open a parent span
	// in the CLI test's context so the registry's WithServerSpan
	// middleware extracts a known TraceID. Asserting the server span
	// inherits that TraceID proves doGET injected the header — a
	// "any span fired" check would pass even if Inject were a no-op.
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	deps := newServerDeps(t, tp.Tracer("test"))
	server := httptest.NewServer(httpapi.NewRouter(deps, deps.Metrics.(*prom.HTTPMetrics).Handler()))
	defer server.Close()

	configurePropagator()
	ctx, parent := tp.Tracer("test").Start(context.Background(), "cli.root")
	defer parent.End()

	args := []string{"artifacts", "--registry", server.URL, "--json"}
	if code := Run(ctx, args, &bytes.Buffer{}, &bytes.Buffer{}, server.Client()); code != 0 {
		t.Fatalf("exit=%d", code)
	}

	wantTrace := parent.SpanContext().TraceID().String()
	var serverSpans []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.SpanKind() == oteltrace.SpanKindServer {
			serverSpans = append(serverSpans, s)
		}
	}
	if len(serverSpans) == 0 {
		t.Fatal("server did not record a SpanKindServer span")
	}
	for _, s := range serverSpans {
		if got := s.SpanContext().TraceID().String(); got != wantTrace {
			t.Fatalf("server span trace=%s want %s — client did not inject traceparent", got, wantTrace)
		}
	}
}

func startServer(t *testing.T) (*httptest.Server, httpapi.Deps) {
	t.Helper()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(tracetest.NewSpanRecorder()))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	deps := newServerDeps(t, tp.Tracer("test"))
	server := httptest.NewServer(httpapi.NewRouter(deps, deps.Metrics.(*prom.HTTPMetrics).Handler()))
	t.Cleanup(server.Close)
	return server, deps
}

func newServerDeps(t *testing.T, tracer oteltrace.Tracer) httpapi.Deps {
	t.Helper()
	logger := jsonlog.New(&bytes.Buffer{}, jsonlog.WithLevel(jsonlog.LevelError))
	return httpapi.Deps{
		AccessLog: logger,
		Metrics:   prom.New(),
		PanicSink: logger,
		Tracer:    tracer,
		Ready:     func() (string, bool) { return "", true },
		Artifacts: memstore.New(),
		EnvState:  memstate.New(),
		Audit:     memaudit.New(),
	}
}
