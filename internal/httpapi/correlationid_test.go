package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/helmedeiros/model-registry/internal/httpapi"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestWithCorrelationIDAcceptsSuppliedHeader(t *testing.T) {
	var seen string
	h := httpapi.WithCorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = httpapi.CorrelationIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(httpapi.CorrelationIDHeader, "supplied-id-abc")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if seen != "supplied-id-abc" {
		t.Fatalf("downstream context id=%q want supplied-id-abc", seen)
	}
	if got := rec.Header().Get(httpapi.CorrelationIDHeader); got != "supplied-id-abc" {
		t.Fatalf("response echo=%q want supplied-id-abc", got)
	}
}

func TestWithCorrelationIDMintsUUIDv4OnMissingHeader(t *testing.T) {
	var seen string
	h := httpapi.WithCorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = httpapi.CorrelationIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if !uuidPattern.MatchString(seen) {
		t.Fatalf("minted id %q does not look like UUID v4", seen)
	}
	if got := rec.Header().Get(httpapi.CorrelationIDHeader); got != seen {
		t.Fatalf("response header %q != context id %q", got, seen)
	}
}

func TestWithCorrelationIDTreatsEmptyHeaderAsMissing(t *testing.T) {
	var seen string
	h := httpapi.WithCorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = httpapi.CorrelationIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(httpapi.CorrelationIDHeader, "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !uuidPattern.MatchString(seen) {
		t.Fatalf("empty header should mint UUID; got %q", seen)
	}
}

func TestCorrelationIDFromContextEmptyWhenUnset(t *testing.T) {
	if got := httpapi.CorrelationIDFromContext(httptest.NewRequest(http.MethodGet, "/x", nil).Context()); got != "" {
		t.Fatalf("unset ctx id=%q want empty", got)
	}
}
