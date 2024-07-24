package httpapi

import "net/http"

// Ready is the closure shape Readyz consumes. Returns a short reason
// describing why the service is not ready (empty when ready) and a
// boolean (true when ready). The cmd shell supplies a closure that
// returns true once the substrate has opened successfully.
type Ready func() (reason string, ready bool)

// Healthz returns a liveness probe handler: 200 on GET if the HTTP
// goroutine is scheduled, 405 with Allow: GET on any other method.
// Always succeeds while the process is responsive; container
// orchestrators use it to detect a deadlocked goroutine that would
// otherwise serve no traffic without exiting.
func Healthz() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
}

// Readyz returns a readiness probe handler: calls ready() on every
// probe; responds 200 {"status":"ready"} when the closure returns
// true, 503 {"status":"not_ready","reason":"..."} when it returns
// false, 405 with Allow: GET on any non-GET method. The orchestrator
// gates traffic on readiness so a pod whose substrate failed to open
// never receives operator requests.
func Readyz(ready Ready) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		reason, ok := ready()
		if ok {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "not_ready",
			"reason": reason,
		})
	})
}
