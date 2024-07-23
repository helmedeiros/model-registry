package otel_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracesdk "go.opentelemetry.io/otel/trace"

	reg "github.com/helmedeiros/model-registry/internal/observability/otel"
)

func TestBootstrapNoneReturnsNoopTracer(t *testing.T) {
	tracer, shutdown, err := reg.Bootstrap(context.Background(), reg.Config{
		Exporter:            reg.ExporterNone,
		InstrumentationName: "test",
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	_, span := tracer.Start(context.Background(), "noop")
	span.End()
	if span.SpanContext().IsValid() {
		t.Fatal("noop tracer must yield invalid span context (no recording)")
	}
}

func TestBootstrapDefaultExporterIsNoop(t *testing.T) {
	tracer, _, err := reg.Bootstrap(context.Background(), reg.Config{InstrumentationName: "test"})
	if err != nil {
		t.Fatalf("Bootstrap with empty exporter: %v", err)
	}
	_, span := tracer.Start(context.Background(), "default")
	span.End()
	if span.SpanContext().IsValid() {
		t.Fatal("default-exporter tracer must be no-op")
	}
}

func TestBootstrapOTLPWithoutEndpointErrors(t *testing.T) {
	_, _, err := reg.Bootstrap(context.Background(), reg.Config{Exporter: reg.ExporterOTLP})
	if err == nil {
		t.Fatal("expected error when OTLP endpoint missing")
	}
}

func TestBootstrapUnknownExporterErrors(t *testing.T) {
	_, _, err := reg.Bootstrap(context.Background(), reg.Config{Exporter: "kafka"})
	if err == nil {
		t.Fatal("expected error for unknown exporter")
	}
}

func TestWithServerSpanNilTracerIsNoOpPassthrough(t *testing.T) {
	called := false
	h := reg.WithServerSpan(nil, "/healthz")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if !called {
		t.Fatal("nil tracer must pass the request through unchanged")
	}
}

func TestWithServerSpanRecordsSpanNameMethodRouteStatus(t *testing.T) {
	tracer, rec := recordingTracer(t)
	wrapped := reg.WithServerSpan(tracer, "/healthz")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	s := spans[0]
	if s.Name() != "registry.http.GET./healthz" {
		t.Fatalf("span name=%q", s.Name())
	}
	if s.SpanKind() != tracesdk.SpanKindServer {
		t.Fatalf("span kind=%v want server", s.SpanKind())
	}
	wantAttrs := map[string]string{
		"http.method":      "GET",
		"http.route":       "/healthz",
		"http.status_code": "200",
	}
	gotAttrs := attrMap(s.Attributes())
	for k, want := range wantAttrs {
		if got := gotAttrs[k]; got != want {
			t.Fatalf("attr %s=%q want %q (got: %+v)", k, got, want, gotAttrs)
		}
	}
}

func TestWithServerSpanFallbackNameWhenRouteEmpty(t *testing.T) {
	tracer, rec := recordingTracer(t)
	wrapped := reg.WithServerSpan(tracer, "")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/admin/anything", nil))

	spans := rec.Ended()
	if spans[0].Name() != "registry.http.POST" {
		t.Fatalf("empty route should drop the route suffix; got %q", spans[0].Name())
	}
}

func TestWithServerSpanMarks5xxAsError(t *testing.T) {
	tracer, rec := recordingTracer(t)
	wrapped := reg.WithServerSpan(tracer, "/store/explode")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/store/explode", nil))

	s := rec.Ended()[0]
	if s.Status().Code != codes.Error {
		t.Fatalf("5xx must mark span Error; got %v", s.Status())
	}
}

func TestWithServerSpan4xxLeavesStatusUnset(t *testing.T) {
	tracer, rec := recordingTracer(t)
	wrapped := reg.WithServerSpan(tracer, "/notfound")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/notfound", nil))

	s := rec.Ended()[0]
	if s.Status().Code == codes.Error {
		t.Fatal("4xx must NOT be marked Error; that's an operator-side miss, not a registry failure")
	}
}

func TestWithServerSpanExtractsParentTraceContext(t *testing.T) {
	// Set a propagator so the test exercises the inbound extract path
	// the production code also takes. Restore the previous one so
	// later tests in the same binary do not inherit a sticky global.
	prev := otelapi.GetTextMapPropagator()
	otelapi.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otelapi.SetTextMapPropagator(prev) })

	tracer, rec := recordingTracer(t)
	wrapped := reg.WithServerSpan(tracer, "/healthz")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	// Synthesized traceparent — version=00, trace_id=11111111…, span_id=2222…, sampled.
	req.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	s := rec.Ended()[0]
	parent := s.Parent()
	if !parent.IsValid() || parent.TraceID().String() != "11111111111111111111111111111111" {
		t.Fatalf("parent trace not extracted: parent=%+v", parent)
	}
}

func recordingTracer(t *testing.T) (tracesdk.Tracer, *tracetest.SpanRecorder) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return tp.Tracer("test"), rec
}

func attrMap(in []attribute.KeyValue) map[string]string {
	out := map[string]string{}
	for _, kv := range in {
		out[string(kv.Key)] = strings.TrimSpace(kv.Value.Emit())
	}
	return out
}
