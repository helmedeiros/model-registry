package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/helmedeiros/model-registry/internal/httpapi"
)

type stubSink struct {
	msg   string
	attrs map[string]any
}

func (s *stubSink) Error(msg string, attrs map[string]any) {
	s.msg = msg
	s.attrs = attrs
}

func TestWithRecoverCatchesPanicAndReturns500(t *testing.T) {
	sink := &stubSink{}
	h := httpapi.WithRecover(sink, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
	if sink.msg != "registry.panic" {
		t.Fatalf("sink.msg=%q want registry.panic", sink.msg)
	}
	if got := sink.attrs["recovered"]; got != "boom" {
		t.Fatalf("recovered=%v want boom", got)
	}
	if got := sink.attrs["path"]; got != "/x" {
		t.Fatalf("path=%v want /x", got)
	}
	if got := sink.attrs["method"]; got != http.MethodGet {
		t.Fatalf("method=%v want GET", got)
	}
	if _, hasStack := sink.attrs["stack"]; !hasStack {
		t.Fatal("expected stack trace attr to be present")
	}
}

func TestWithRecoverCarriesCorrelationIDIntoPanicAttrs(t *testing.T) {
	sink := &stubSink{}
	h := httpapi.WithCorrelationID(httpapi.WithRecover(sink, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("kaboom")
	})))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(httpapi.CorrelationIDHeader, "cid-42")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := sink.attrs["correlation_id"]; got != "cid-42" {
		t.Fatalf("correlation_id=%v want cid-42", got)
	}
}

func TestWithRecoverPassesThroughOnNormalHandlers(t *testing.T) {
	sink := &stubSink{}
	h := httpapi.WithRecover(sink, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status=%d want 418 — recover middleware must not interfere with non-panicking handlers", rec.Code)
	}
	if sink.msg != "" {
		t.Fatalf("sink should not fire when no panic occurred; got %q", sink.msg)
	}
}

func TestWithRecoverPanicsAtConstructionOnNilSink(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil sink")
		}
	}()
	httpapi.WithRecover(nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
}
