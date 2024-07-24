package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/helmedeiros/model-registry/internal/httpapi"
)

func TestHealthzGetReturns200StatusOk(t *testing.T) {
	rec := httptest.NewRecorder()
	httpapi.Healthz().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	if got, _ := bodyField(t, rec.Body, "status"); got != "ok" {
		t.Fatalf("status field=%q want ok", got)
	}
}

func TestHealthzWrongMethodReturns405WithAllow(t *testing.T) {
	rec := httptest.NewRecorder()
	httpapi.Healthz().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow=%q want GET", got)
	}
}

func TestReadyzReadyClosureReturns200(t *testing.T) {
	ready := func() (string, bool) { return "", true }
	rec := httptest.NewRecorder()
	httpapi.Readyz(ready).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	if got, _ := bodyField(t, rec.Body, "status"); got != "ready" {
		t.Fatalf("status field=%q want ready", got)
	}
}

func TestReadyzNotReadyReturns503WithReason(t *testing.T) {
	ready := func() (string, bool) { return "substrate opening", false }
	rec := httptest.NewRecorder()
	httpapi.Readyz(ready).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", rec.Code)
	}
	parsed := decodeBody(t, rec.Body)
	if parsed["status"] != "not_ready" {
		t.Fatalf("status=%v want not_ready", parsed["status"])
	}
	if parsed["reason"] != "substrate opening" {
		t.Fatalf("reason=%v want substrate opening", parsed["reason"])
	}
}

func TestReadyzWrongMethodReturns405WithAllow(t *testing.T) {
	ready := func() (string, bool) { return "", true }
	rec := httptest.NewRecorder()
	httpapi.Readyz(ready).ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/readyz", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow=%q want GET", got)
	}
}

func bodyField(t *testing.T, r io.Reader, key string) (string, map[string]any) {
	t.Helper()
	parsed := decodeBody(t, r)
	v, _ := parsed[key].(string)
	return v, parsed
}

func decodeBody(t *testing.T, r io.Reader) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return out
}
