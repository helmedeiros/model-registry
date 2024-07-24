// Package httpapi composes the model-registry HTTP surface: the
// /healthz + /readyz probes plus the middleware chain ADR-0003
// commits to (recover → correlation-id → trace-context → access-log
// → metrics → handler).
package httpapi

import (
	"encoding/json"
	"net/http"
)

// CorrelationIDHeader is the HTTP header name carrying the cross-system
// trace identifier on both the request (caller-supplied) and the
// response (echoed back). Same name every service in the platform arc
// uses so a Kibana search on attrs.correlation_id joins requests
// across services.
const CorrelationIDHeader = "X-Correlation-ID"

// writeJSON encodes v with a Content-Type: application/json header.
// Status defaults to 200 when WriteHeader was not already called.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		w.WriteHeader(status)
	}
	_ = json.NewEncoder(w).Encode(v)
}

// writeError emits the platform-standard error envelope {status, reason}.
// status is the HTTP status code; reason is a short opaque tag so error
// classes can be aggregated without leaking internal detail to the
// caller.
func writeError(w http.ResponseWriter, status int, reason string) {
	writeJSON(w, status, map[string]string{
		"status": "error",
		"reason": reason,
	})
}
