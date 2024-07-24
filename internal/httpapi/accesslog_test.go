package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/helmedeiros/model-registry/internal/httpapi"
)

type accessSink struct {
	msg   string
	attrs map[string]any
}

func (a *accessSink) Info(msg string, attrs map[string]any) {
	a.msg = msg
	a.attrs = attrs
}

func TestWithAccessLogEmitsCoreAttrs(t *testing.T) {
	sink := &accessSink{}
	h := httpapi.WithAccessLog(sink, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe", nil))

	if sink.msg != "registry.access" {
		t.Fatalf("msg=%q want registry.access", sink.msg)
	}
	if sink.attrs["method"] != http.MethodGet {
		t.Fatalf("method=%v", sink.attrs["method"])
	}
	if sink.attrs["path"] != "/probe" {
		t.Fatalf("path=%v", sink.attrs["path"])
	}
	if sink.attrs["status"] != http.StatusTeapot {
		t.Fatalf("status=%v want 418", sink.attrs["status"])
	}
	if _, ok := sink.attrs["duration_ms"].(float64); !ok {
		t.Fatalf("duration_ms missing or wrong type: %v", sink.attrs["duration_ms"])
	}
	if _, hasCID := sink.attrs["correlation_id"]; hasCID {
		t.Fatal("correlation_id should be absent when not set on context")
	}
	if _, hasTID := sink.attrs["trace_id"]; hasTID {
		t.Fatal("trace_id should be absent when no span is on context")
	}
}

func TestWithAccessLogCarriesCorrelationIDFromContext(t *testing.T) {
	sink := &accessSink{}
	h := httpapi.WithCorrelationID(httpapi.WithAccessLog(sink, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(httpapi.CorrelationIDHeader, "cid-7")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if sink.attrs["correlation_id"] != "cid-7" {
		t.Fatalf("correlation_id=%v want cid-7", sink.attrs["correlation_id"])
	}
}

func TestWithAccessLogIncludesTraceIDWhenSpanInContext(t *testing.T) {
	sink := &accessSink{}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(tracetest.NewSpanRecorder()))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tracer := tp.Tracer("test")

	h := httpapi.WithAccessLog(sink, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	ctx, span := tracer.Start(context.Background(), "test.parent", oteltrace.WithSpanKind(oteltrace.SpanKindServer))
	defer span.End()
	req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	traceID, _ := sink.attrs["trace_id"].(string)
	spanID, _ := sink.attrs["span_id"].(string)
	if traceID == "" || spanID == "" {
		t.Fatalf("trace_id=%q span_id=%q", traceID, spanID)
	}
	if traceID != span.SpanContext().TraceID().String() {
		t.Fatalf("trace_id=%s want %s", traceID, span.SpanContext().TraceID())
	}
}

func TestWithAccessLogPanicsAtConstructionOnNilSink(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil sink")
		}
	}()
	httpapi.WithAccessLog(nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
}
