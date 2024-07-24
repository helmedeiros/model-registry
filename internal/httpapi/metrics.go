package httpapi

import (
	"net/http"
	"strconv"
	"time"
)

// MetricsRecorder is the minimal interface WithMetrics needs. The
// prom.HTTPMetrics adapter satisfies it; tests inject a stub.
type MetricsRecorder interface {
	RecordRequest(method, path, status string, duration time.Duration)
}

// WithMetrics records one (method, path, status, duration) sample per
// served request via the supplied MetricsRecorder. path MUST be the
// matched-route template (e.g. "/healthz"); the Router that mounts
// this middleware is the compile-time owner of that mapping —
// passing r.URL.Path on a parameterised path explodes
// registry_http_requests_total cardinality.
func WithMetrics(rec MetricsRecorder, route string, next http.Handler) http.Handler {
	if rec == nil {
		panic("httpapi.WithMetrics: recorder required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		rec.RecordRequest(r.Method, route, strconv.Itoa(sw.status), time.Since(start))
	})
}
