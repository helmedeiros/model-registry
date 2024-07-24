package httpapi_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/helmedeiros/model-registry/internal/httpapi"
)

func TestRouterServesHealthzReadyzMetrics(t *testing.T) {
	deps, scrape := newRouterDeps(t)
	r := httpapi.NewRouter(deps, http.HandlerFunc(scrape))

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/healthz", http.StatusOK},
		{"/readyz", http.StatusOK},
		{"/metrics", http.StatusOK},
	} {
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rr.Code != tc.want {
			t.Fatalf("%s: status=%d want %d", tc.path, rr.Code, tc.want)
		}
	}
}

func TestRouterChainWiresAllObservabilityHooks(t *testing.T) {
	deps, scrape := newRouterDeps(t)
	r := httpapi.NewRouter(deps, http.HandlerFunc(scrape))

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	// Access log fired with the matched route.
	access := deps.AccessLog.(*accessSink)
	if access.msg != "registry.access" || access.attrs["path"] != "/healthz" {
		t.Fatalf("access log not invoked correctly: msg=%q attrs=%+v", access.msg, access.attrs)
	}
	// Metrics recorded the matched route, not raw URI.
	metrics := deps.Metrics.(*metricsStub)
	if len(metrics.calls) != 1 || metrics.calls[0].route != "/healthz" {
		t.Fatalf("metrics not invoked correctly: %+v", metrics.calls)
	}
	// Correlation ID minted on the response.
	if rr.Header().Get(httpapi.CorrelationIDHeader) == "" {
		t.Fatal("correlation id middleware did not echo a value")
	}
	// Trace span attributes flowed into the access log.
	if access.attrs["trace_id"] == nil {
		t.Fatal("trace_id missing — WithServerSpan did not run before WithAccessLog")
	}
}

func TestRouterMetricsScrapeDoesNotRecordItself(t *testing.T) {
	deps, scrape := newRouterDeps(t)
	r := httpapi.NewRouter(deps, http.HandlerFunc(scrape))

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	metrics := deps.Metrics.(*metricsStub)
	if len(metrics.calls) != 0 {
		t.Fatalf("/metrics scrape should not feed itself: got %+v", metrics.calls)
	}
	// Access log still fires so the exposition path is visible in Kibana.
	access := deps.AccessLog.(*accessSink)
	if access.msg != "registry.access" || access.attrs["path"] != "/metrics" {
		t.Fatalf("access log should observe /metrics scrapes too: %+v", access.attrs)
	}
}

func TestRouterRecoversFromPanickingHandler(t *testing.T) {
	deps, _ := newRouterDeps(t)
	// Stub a /metrics handler that panics on use so the recover middleware
	// has something to catch in this composition test.
	panicker := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("synthetic exposition error")
	})
	r := httpapi.NewRouter(deps, panicker)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("recovered panic should map to 500; got %d", rr.Code)
	}
	if deps.PanicSink.(*stubSink).msg != "registry.panic" {
		t.Fatalf("panic sink not fired: %+v", deps.PanicSink)
	}
}

func newRouterDeps(t *testing.T) (httpapi.Deps, http.HandlerFunc) {
	t.Helper()
	// Use the SDK with an in-memory span recorder so the chain test can
	// verify trace_id flows from WithServerSpan into WithAccessLog. The
	// no-op tracer the production Bootstrap returns when exporter=none
	// would yield invalid SpanContexts and not exercise the wiring.
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(tracetest.NewSpanRecorder()))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	deps := httpapi.Deps{
		AccessLog: &accessSink{},
		Metrics:   &metricsStub{},
		PanicSink: &stubSink{},
		Tracer:    tp.Tracer("router-test"),
		Ready:     func() (string, bool) { return "", true },
	}
	scrape := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "# metric exposition stub\n")
	}
	return deps, scrape
}
