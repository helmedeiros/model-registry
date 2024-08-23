package httpapi_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/httpapi"
)

// duplicateIDAuditWriter rejects every Record with ErrDuplicateID so
// the upload handler reaches its audit-failure swallow path on a
// happy-path /upload call.
type duplicateIDAuditWriter struct{}

func (duplicateIDAuditWriter) Record(_ context.Context, _ audit.Entry) error {
	return audit.ErrDuplicateID
}

// captureLogger records every event so the test can inspect the attrs
// map for trace_id + span_id correlation.
type captureLogger struct {
	mu   sync.Mutex
	logs []capturedEvent
}

type capturedEvent struct {
	msg   string
	attrs map[string]any
}

func (c *captureLogger) Info(msg string, attrs map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, capturedEvent{msg: msg, attrs: attrs})
}

func (c *captureLogger) Error(msg string, attrs map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, capturedEvent{msg: msg, attrs: attrs})
}

// TestHandlerLogEventsCarryTraceID drives the audit-failure log path
// (via a failing audit writer) and asserts the resulting event carries
// trace_id + span_id so a Kibana → Jaeger hop is two clicks.
//
// We exercise the upload handler because its happy path always hits
// the audit-record block; the failure mode is injected via an Audit
// writer that always returns ErrDuplicateID. The handler swallows
// the error (Put already committed) and logs `registry.audit.write_failed`.
// The test asserts that log line is trace-correlated.
func mustReadAll(r io.Reader) []byte {
	b, _ := io.ReadAll(r)
	return b
}

// TestAuditEntryCarriesTraceID drives a /promote and asserts the
// recorded audit.Entry's TraceID equals the parent trace's TraceID.
// Without this, the /audit endpoint and the Jaeger UI are two
// disjoint surfaces — operators can read who promoted what, or look
// at a trace, but cannot hop from one to the other.
func TestAuditEntryCarriesTraceID(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	deps, st, _, au, _ := newPromoteDeps(t, okResult("http://markup-svc-1:8080"))
	h := putRule(t, st, []byte("alpha,rule,1.0,1\n"))

	ctx, span := tp.Tracer("operator").Start(context.Background(), "operator.promote")
	defer span.End()

	body := promoteBody(t, httpapi.PromoteRequest{
		Hash: string(h), Env: "production", Role: "champion", Operator: "alice", Reason: "trace-test",
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/promote", body).WithContext(ctx)
	httpapi.Promote(deps).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("/promote: %d %s", rr.Code, rr.Body.String())
	}

	page, err := au.List(context.Background(), audit.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("audit entries=%d want 1", len(page.Items))
	}
	want := span.SpanContext().TraceID().String()
	if got := page.Items[0].TraceID; got != want {
		t.Fatalf("audit TraceID=%q want %q", got, want)
	}
}

func TestHandlerLogEventsCarryTraceID(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	deps, _, _, _ := newUploadDeps(t)
	deps.Audit = duplicateIDAuditWriter{}
	logger := &captureLogger{}
	deps.Logger = logger

	ctx, span := tp.Tracer("operator").Start(context.Background(), "operator.upload")
	defer span.End()

	body, ct := multipartBody(t, map[string]uploadPart{
		"source": {filename: "rules.csv", contentType: "text/csv", body: []byte("alpha,rule,1.0,1\n")},
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/upload", body).WithContext(ctx)
	req.Header.Set("Content-Type", ct)
	httpapi.Upload(deps).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("/upload happy path returned %d body=%s", rr.Code, mustReadAll(rr.Body))
	}

	if len(logger.logs) == 0 {
		t.Fatal("expected an audit.write_failed log event")
	}
	wantTrace := span.SpanContext().TraceID().String()
	wantSpan := span.SpanContext().SpanID().String()
	for _, e := range logger.logs {
		if e.msg != "registry.audit.write_failed" {
			continue
		}
		gotTrace, _ := e.attrs["trace_id"].(string)
		gotSpan, _ := e.attrs["span_id"].(string)
		if gotTrace != wantTrace {
			t.Fatalf("trace_id=%q want %q", gotTrace, wantTrace)
		}
		// span_id is the child span the handler opens, NOT the test's
		// parent. Just assert it is present and non-empty.
		if gotSpan == "" || gotSpan == wantSpan {
			t.Fatalf("span_id=%q (parent=%q) — want a non-empty child span id", gotSpan, wantSpan)
		}
		return
	}
	t.Fatalf("audit.write_failed event not found in %d logs", len(logger.logs))
}
