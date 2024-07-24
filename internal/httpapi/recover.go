package httpapi

import (
	"fmt"
	"net/http"
	"runtime/debug"
)

// PanicSink is the minimal interface WithRecover needs from the
// observability logger. The middleware does not depend on a concrete
// *jsonlog.Logger so test doubles stay one method wide.
type PanicSink interface {
	Error(msg string, attrs map[string]any)
}

// WithRecover catches panics in downstream handlers, logs them as
// registry.panic with the recovered value + stack trace + correlation
// ID, and responds 500 with an opaque body. The outermost middleware
// in the chain — placing it any deeper would leak panics through the
// middlewares above it. sink must be non-nil; a nil sink would silently
// swallow production diagnostics on the one code path that surfaces
// them.
func WithRecover(sink PanicSink, next http.Handler) http.Handler {
	if sink == nil {
		panic("httpapi.WithRecover: sink required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				sink.Error("registry.panic", map[string]any{
					"recovered":      fmt.Sprintf("%v", rec),
					"path":           r.URL.Path,
					"method":         r.Method,
					"correlation_id": CorrelationIDFromContext(r.Context()),
					"stack":          string(debug.Stack()),
				})
				writeError(w, http.StatusInternalServerError, "internal")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
