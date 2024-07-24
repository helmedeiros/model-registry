package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/httpapi"
)

type metricsStub struct {
	calls []metricsCall
}

type metricsCall struct {
	method, route, status string
	duration              time.Duration
}

func (m *metricsStub) RecordRequest(method, route, status string, d time.Duration) {
	m.calls = append(m.calls, metricsCall{method, route, status, d})
}

func TestWithMetricsRecordsMethodRouteStatus(t *testing.T) {
	rec := &metricsStub{}
	h := httpapi.WithMetrics(rec, "/healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if len(rec.calls) != 1 {
		t.Fatalf("calls=%d want 1", len(rec.calls))
	}
	c := rec.calls[0]
	if c.method != http.MethodGet || c.route != "/healthz" || c.status != "200" {
		t.Fatalf("unexpected call: %+v", c)
	}
	if c.duration <= 0 {
		t.Fatalf("duration must be positive: %v", c.duration)
	}
}

func TestWithMetricsRecordsRouteTemplateNotRawURI(t *testing.T) {
	// If the future router passes a route template like "/artifacts/{hash}",
	// the recorder must see the template, not the raw URI from r.URL.Path.
	rec := &metricsStub{}
	h := httpapi.WithMetrics(rec, "/artifacts/:hash", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/artifacts/0d8a", nil))

	if rec.calls[0].route != "/artifacts/:hash" {
		t.Fatalf("route=%q want /artifacts/:hash", rec.calls[0].route)
	}
	if rec.calls[0].status != "404" {
		t.Fatalf("status=%q want 404", rec.calls[0].status)
	}
}

func TestWithMetricsPanicsAtConstructionOnNilRecorder(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil recorder")
		}
	}()
	httpapi.WithMetrics(nil, "/x", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
}
