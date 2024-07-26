package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/httpapi"
	"github.com/helmedeiros/model-registry/internal/store"
	"github.com/helmedeiros/model-registry/internal/store/memstore"
)

func TestArtifactsReturnsEmptyPageOnFreshStore(t *testing.T) {
	s := memstore.New()
	rec := httptest.NewRecorder()
	httpapi.Artifacts(s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifacts", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	page := decodeArtifactPage(t, rec.Body)
	if len(page.Items) != 0 {
		t.Fatalf("fresh store should produce empty page; got %d items", len(page.Items))
	}
	if page.NextCursor != "" {
		t.Fatalf("empty store should not produce NextCursor; got %q", page.NextCursor)
	}
}

func TestArtifactsListsCurrentItems(t *testing.T) {
	s := memstore.New(memstore.WithClock(stepClock()))
	hashes := []store.Hash{
		mustPut(t, s, "alpha", "ci-bot"),
		mustPut(t, s, "beta", "ci-bot"),
	}

	rec := httptest.NewRecorder()
	httpapi.Artifacts(s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifacts", nil))

	page := decodeArtifactPage(t, rec.Body)
	if len(page.Items) != 2 {
		t.Fatalf("want 2 items, got %d", len(page.Items))
	}
	// newest-first ordering (created_at DESC).
	if page.Items[0].Hash != string(hashes[1]) || page.Items[1].Hash != string(hashes[0]) {
		t.Fatalf("ordering broken: %s, %s", page.Items[0].Hash, page.Items[1].Hash)
	}
	if page.Items[0].State != "staged" {
		t.Fatalf("state=%q want staged", page.Items[0].State)
	}
	if page.Items[0].ContentType != "text/csv" {
		t.Fatalf("content_type=%q want text/csv", page.Items[0].ContentType)
	}
	if page.Items[0].Metadata.CreatedBy != "ci-bot" {
		t.Fatalf("created_by=%q want ci-bot", page.Items[0].Metadata.CreatedBy)
	}
	if page.Items[0].Metadata.CreatedAt == "" {
		t.Fatal("created_at must be populated")
	}
}

func TestArtifactsPaginatesViaCursor(t *testing.T) {
	s := memstore.New(memstore.WithClock(stepClock()))
	for _, name := range []string{"a", "b", "c"} {
		mustPut(t, s, name, "ci-bot")
	}

	rec1 := httptest.NewRecorder()
	httpapi.Artifacts(s).ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/artifacts?limit=2", nil))
	page1 := decodeArtifactPage(t, rec1.Body)
	if len(page1.Items) != 2 || page1.NextCursor == "" {
		t.Fatalf("first page: items=%d next=%q", len(page1.Items), page1.NextCursor)
	}

	rec2 := httptest.NewRecorder()
	httpapi.Artifacts(s).ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/artifacts?limit=2&cursor="+page1.NextCursor, nil))
	page2 := decodeArtifactPage(t, rec2.Body)
	if len(page2.Items) != 1 || page2.NextCursor != "" {
		t.Fatalf("second page: items=%d next=%q", len(page2.Items), page2.NextCursor)
	}
}

func TestArtifactsAppliesStateFilter(t *testing.T) {
	s := memstore.New(memstore.WithClock(stepClock()))
	hStaged := mustPut(t, s, "staged-one", "ci-bot")
	hActive := mustPut(t, s, "active-one", "ci-bot")
	if err := s.Tag(context.Background(), "release", hActive); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	httpapi.Artifacts(s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifacts?state=active", nil))
	page := decodeArtifactPage(t, rec.Body)
	if len(page.Items) != 1 || page.Items[0].Hash != string(hActive) {
		t.Fatalf("state=active: %+v want hash %s", page.Items, hActive)
	}

	rec = httptest.NewRecorder()
	httpapi.Artifacts(s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifacts?state=staged", nil))
	page = decodeArtifactPage(t, rec.Body)
	if len(page.Items) != 1 || page.Items[0].Hash != string(hStaged) {
		t.Fatalf("state=staged: %+v want hash %s", page.Items, hStaged)
	}
}

func TestArtifactsRejectsInvalidState(t *testing.T) {
	rec := httptest.NewRecorder()
	httpapi.Artifacts(memstore.New()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifacts?state=zombie", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "invalid_state" {
		t.Fatalf("reason=%q want invalid_state", got)
	}
}

func TestArtifactsRejectsInvalidLimit(t *testing.T) {
	rec := httptest.NewRecorder()
	httpapi.Artifacts(memstore.New()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifacts?limit=many", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "invalid_limit" {
		t.Fatalf("reason=%q want invalid_limit", got)
	}
}

func TestArtifactsRejectsNegativeLimit(t *testing.T) {
	rec := httptest.NewRecorder()
	httpapi.Artifacts(memstore.New()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifacts?limit=-1", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestArtifactsRejectsWrongMethod(t *testing.T) {
	rec := httptest.NewRecorder()
	httpapi.Artifacts(memstore.New()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/artifacts", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow=%q want GET", got)
	}
}

func TestArtifactsPanicsAtConstructionOnNilReader(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil reader")
		}
	}()
	httpapi.Artifacts(nil)
}

func mustPut(t *testing.T, s store.Store, name, by string) store.Hash {
	t.Helper()
	h, err := s.Put(context.Background(), store.PutRequest{
		SourceBytes: []byte("rule=" + name),
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedBy: by, Description: name},
	})
	if err != nil {
		t.Fatalf("Put(%s): %v", name, err)
	}
	return h
}

func stepClock() func() time.Time {
	t := time.Unix(0, 0).UTC()
	return func() time.Time {
		t = t.Add(time.Millisecond)
		return t
	}
}

func decodeArtifactPage(t *testing.T, r io.Reader) httpapi.ArtifactPage {
	t.Helper()
	var p httpapi.ArtifactPage
	if err := json.NewDecoder(r).Decode(&p); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	return p
}

func bodyReason(t *testing.T, r io.Reader) string {
	t.Helper()
	var env map[string]string
	if err := json.NewDecoder(r).Decode(&env); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return env["reason"]
}
