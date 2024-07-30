package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/httpapi"
)

type stubAuditReader struct {
	page audit.Page
}

func (s stubAuditReader) List(_ context.Context, _ audit.ListOptions) (audit.Page, error) {
	return s.page, nil
}

func TestAuditReturnsEmptyPageOnFreshStore(t *testing.T) {
	rec := httptest.NewRecorder()
	httpapi.Audit(stubAuditReader{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/audit", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	page := decodeAuditPage(t, rec.Body)
	if len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatalf("fresh store should be empty: %+v", page)
	}
}

func TestAuditSerializesPopulatedEntries(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	reader := stubAuditReader{page: audit.Page{
		Items: []audit.Entry{
			{ID: "01HXY", Operator: "alice", Action: "promote", Target: "env/production/champion", ArtifactHash: "abc", Reason: "weekly canary", At: at},
		},
	}}

	rec := httptest.NewRecorder()
	httpapi.Audit(reader).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/audit", nil))
	page := decodeAuditPage(t, rec.Body)
	if len(page.Items) != 1 {
		t.Fatalf("want 1 entry, got %d", len(page.Items))
	}
	e := page.Items[0]
	if e.ID != "01HXY" || e.Operator != "alice" || e.Action != "promote" {
		t.Fatalf("entry: %+v", e)
	}
	if e.ArtifactHash != "abc" || e.Reason != "weekly canary" {
		t.Fatalf("optional fields: %+v", e)
	}
	if e.At == "" {
		t.Fatal("at must be populated")
	}
}

func TestAuditRejectsInvalidLimit(t *testing.T) {
	rec := httptest.NewRecorder()
	httpapi.Audit(stubAuditReader{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/audit?limit=many", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "invalid_limit" {
		t.Fatalf("reason=%q want invalid_limit", got)
	}
}

func TestAuditRejectsOverLimit(t *testing.T) {
	rec := httptest.NewRecorder()
	httpapi.Audit(stubAuditReader{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/audit?limit=5000", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "limit_too_large" {
		t.Fatalf("reason=%q want limit_too_large", got)
	}
}

func TestAuditRejectsWrongMethod(t *testing.T) {
	rec := httptest.NewRecorder()
	httpapi.Audit(stubAuditReader{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/audit", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
	if rec.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("Allow=%q want GET", rec.Header().Get("Allow"))
	}
}

func TestAuditPanicsAtConstructionOnNilReader(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	httpapi.Audit(nil)
}

func decodeAuditPage(t *testing.T, r io.Reader) httpapi.AuditPage {
	t.Helper()
	var p httpapi.AuditPage
	if err := json.NewDecoder(r).Decode(&p); err != nil {
		t.Fatalf("decode audit page: %v", err)
	}
	return p
}
