package prom_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/observability/metrics/prom"
)

func TestHandlerServes200OnEmptyExposition(t *testing.T) {
	// Prometheus exposition is lazy: HELP / TYPE lines appear only after
	// a labelset has been observed. Before any RecordRequest the handler
	// still serves 200 with the standard exposition Content-Type so a
	// Prometheus scrape against a fresh process succeeds.
	m := prom.New()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type=%q want text/plain prefix", ct)
	}
}

func TestRecordRequestIncrementsCounterAndHistogram(t *testing.T) {
	m := prom.New()
	m.RecordRequest(http.MethodGet, "/healthz", "200", 150*time.Microsecond)
	m.RecordRequest(http.MethodGet, "/healthz", "200", 220*time.Microsecond)
	m.RecordRequest(http.MethodGet, "/readyz", "503", 1500*time.Microsecond)

	out := exposition(t, m)

	for _, line := range []string{
		`registry_http_requests_total{method="GET",path="/healthz",status="200"} 2`,
		`registry_http_requests_total{method="GET",path="/readyz",status="503"} 1`,
		`registry_http_request_duration_seconds_count{method="GET",path="/healthz"} 2`,
		`registry_http_request_duration_seconds_count{method="GET",path="/readyz"} 1`,
	} {
		if !strings.Contains(out, line) {
			t.Fatalf("expected line %q in exposition\n%s", line, out)
		}
	}
}

func TestSeparateInstancesDoNotShareState(t *testing.T) {
	a := prom.New()
	b := prom.New()
	a.RecordRequest(http.MethodGet, "/healthz", "200", time.Microsecond)

	if !strings.Contains(exposition(t, a), `registry_http_requests_total{method="GET",path="/healthz",status="200"} 1`) {
		t.Fatal("a should observe its own record")
	}
	if strings.Contains(exposition(t, b), `registry_http_requests_total{method="GET",path="/healthz",status="200"}`) {
		t.Fatal("b should not see a's records")
	}
}

func exposition(t *testing.T, m *prom.HTTPMetrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	return string(body)
}
