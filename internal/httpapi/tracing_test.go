package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/helmedeiros/model-registry/internal/httpapi"
)

// TestPromoteHandlerEmitsLifecycleChildSpans asserts that a /promote
// call records the new handler-level child spans
// (registry.champion.commit_state, registry.audit.record) under the
// parent span. Without these the trace collapses to one HTTP span and
// operators cannot localise a slow promote.
func TestPromoteHandlerEmitsLifecycleChildSpans(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	// Install the recording TP as the global so handler code that uses
	// otel.Tracer(...) records into the same recorder.
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	deps, st, _, _, _ := newPromoteDeps(t, okResult("http://markup-svc-1:8080"))
	h := putRule(t, st, []byte("alpha,rule,1.0,1\n"))

	ctx, parent := tp.Tracer("test").Start(context.Background(), "operator.promote")
	defer parent.End()

	body, _ := json.Marshal(httpapi.PromoteRequest{
		Hash: string(h), Env: "production", Role: "champion", Operator: "alice", Reason: "trace-test",
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/promote", bytes.NewReader(body)).WithContext(ctx)
	httpapi.Promote(deps).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("/promote: %d %s", rr.Code, rr.Body.String())
	}

	parent.End()
	wantTrace := parent.SpanContext().TraceID().String()
	wantSpanNames := []string{"registry.champion.commit_state", "registry.audit.record"}
	got := map[string]bool{}
	for _, s := range rec.Ended() {
		if s.SpanContext().TraceID().String() == wantTrace {
			got[s.Name()] = true
		}
	}
	for _, want := range wantSpanNames {
		if !got[want] {
			t.Fatalf("missing span %q; got: %v", want, spanNamesIn(rec))
		}
	}
}

// TestRollbackHandlerEmitsLifecycleChildSpans asserts the rollback
// handler emits commit_state + audit.record spans alongside the
// HTTP server span.
func TestRollbackHandlerEmitsLifecycleChildSpans(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	deps, st, envState, _, _ := newRollbackDeps(t, okResult("http://markup-svc-1:8080"))
	h1 := putRule(t, st, []byte("alpha,rule,1.0,1\n"))
	h2 := putRule(t, st, []byte("beta,rule,1.0,1\n"))
	for _, h := range []oteltrace.SpanContext{} {
		_ = h
	}
	if _, err := envState.PromoteChampion(context.Background(), "production", h1, "ci-bot", "seed-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := envState.PromoteChampion(context.Background(), "production", h2, "ci-bot", "seed-b"); err != nil {
		t.Fatal(err)
	}

	ctx, parent := tp.Tracer("test").Start(context.Background(), "operator.rollback")
	defer parent.End()

	body, _ := json.Marshal(httpapi.RollbackRequest{
		Env: "production", Operator: "alice", Reason: "h2 misbehaved",
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/rollback", bytes.NewReader(body)).WithContext(ctx)
	httpapi.Rollback(deps).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("/rollback: %d %s", rr.Code, rr.Body.String())
	}

	parent.End()
	wantTrace := parent.SpanContext().TraceID().String()
	wantSpanNames := []string{"registry.champion.commit_state", "registry.audit.record"}
	got := map[string]bool{}
	for _, s := range rec.Ended() {
		if s.SpanContext().TraceID().String() == wantTrace {
			got[s.Name()] = true
		}
	}
	for _, want := range wantSpanNames {
		if !got[want] {
			t.Fatalf("missing span %q; got: %v", want, spanNamesIn(rec))
		}
	}
}

func spanNamesIn(rec *tracetest.SpanRecorder) []string {
	names := make([]string, 0, len(rec.Ended()))
	for _, s := range rec.Ended() {
		names = append(names, s.Name())
	}
	return names
}
