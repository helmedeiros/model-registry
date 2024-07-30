package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/httpapi"
)

type seededAuditReader struct {
	page audit.Page
}

func (s seededAuditReader) List(_ context.Context, _ audit.ListOptions) (audit.Page, error) {
	return s.page, nil
}

// BenchmarkGET_Audit_100Entries pins the per-request cost of /audit
// against ADR-0004's < 50 ms bar at the page-size scale. The
// substrate cost is captured separately in memaudit benches.
func BenchmarkGET_Audit_100Entries(b *testing.B) {
	at := time.Unix(1_700_000_000, 0).UTC()
	items := make([]audit.Entry, 100)
	for i := range items {
		items[i] = audit.Entry{
			ID:       strconv.Itoa(i),
			Operator: "alice",
			Action:   "promote",
			Target:   "env/production/champion",
			At:       at.Add(time.Duration(i) * time.Millisecond),
		}
	}
	reader := seededAuditReader{page: audit.Page{Items: items}}
	handler := httpapi.Audit(reader)
	req := httptest.NewRequest(http.MethodGet, "/audit?limit=100", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}
