package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// White-box test colocated with the package so randRead can be
// swapped without exporting the seam. Single-threaded; tests in this
// package do not call t.Parallel().
func TestWithCorrelationIDReturns500WhenRandFails(t *testing.T) {
	prev := randRead
	randRead = func([]byte) (int, error) { return 0, errors.New("entropy starvation") }
	t.Cleanup(func() { randRead = prev })

	h := WithCorrelationID(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("downstream should not run when UUID generation fails")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
}
