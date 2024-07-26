package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/helmedeiros/model-registry/internal/httpapi"
	"github.com/helmedeiros/model-registry/internal/store"
	"github.com/helmedeiros/model-registry/internal/store/memstore"
)

func TestArtifactReturnsBundleForKnownHash(t *testing.T) {
	s := memstore.New(memstore.WithClock(stepClock()))
	h, err := s.Put(context.Background(), store.PutRequest{
		SourceBytes:   []byte("alpha,rule,1.0,1\n"),
		ContentType:   store.ContentTypeCSV,
		SnapshotBytes: []byte("{}"),
		Metadata:      store.Metadata{CreatedBy: "ci-bot", Description: "alpha"},
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/artifact/"+string(h), nil)
	req.SetPathValue("hash", string(h))
	httpapi.Artifact(s).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	got := decodeBundle(t, rec.Body)
	if got.Hash != string(h) {
		t.Fatalf("hash=%q want %q", got.Hash, h)
	}
	if got.ContentType != "text/csv" {
		t.Fatalf("content_type=%q", got.ContentType)
	}
	if !got.HasSnapshot || got.HasDiagnose {
		t.Fatalf("flags: snapshot=%v diagnose=%v", got.HasSnapshot, got.HasDiagnose)
	}
	if got.Metadata.CreatedBy != "ci-bot" || got.Metadata.Description != "alpha" {
		t.Fatalf("metadata: %+v", got.Metadata)
	}
}

func TestArtifactUnknownHashReturns404NotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/artifact/none", nil)
	req.SetPathValue("hash", "none")
	httpapi.Artifact(memstore.New()).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "not_found" {
		t.Fatalf("reason=%q want not_found", got)
	}
}

func TestArtifactMissingHashReturns400(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/artifact/", nil)
	httpapi.Artifact(memstore.New()).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestArtifactRejectsWrongMethod(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/artifact/abc", nil)
	req.SetPathValue("hash", "abc")
	httpapi.Artifact(memstore.New()).ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
	if rec.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("Allow=%q", rec.Header().Get("Allow"))
	}
}

func TestArtifactPanicsAtConstructionOnNilReader(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	httpapi.Artifact(nil)
}

// --- /artifact/{hash}/{member} ---

func TestArtifactMemberSourceReturnsBytesWithDeclaredContentType(t *testing.T) {
	s := memstore.New(memstore.WithClock(stepClock()))
	src := []byte("alpha,rule,1.0,1\n")
	h := mustPutMember(t, s, src, []byte("snap"), []byte("diag"))

	rec, req := newMemberRequest(string(h), string(store.MemberSource))
	httpapi.ArtifactMember(s).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if string(body) != string(src) {
		t.Fatalf("body=%q want %q", body, src)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/csv" {
		t.Fatalf("Content-Type=%q want text/csv", got)
	}
	if got := rec.Header().Get("X-Artifact-Hash"); got != string(h) {
		t.Fatalf("X-Artifact-Hash=%q want %q", got, h)
	}
	if got := rec.Header().Get("Content-Length"); got == "" {
		t.Fatal("Content-Length must be populated")
	}
}

func TestArtifactMemberSnapshotReturnsOctetStream(t *testing.T) {
	s := memstore.New(memstore.WithClock(stepClock()))
	snap := []byte(`{"v":1}`)
	h := mustPutMember(t, s, []byte("src"), snap, nil)

	rec, req := newMemberRequest(string(h), string(store.MemberSnapshot))
	httpapi.ArtifactMember(s).ServeHTTP(rec, req)

	body, _ := io.ReadAll(rec.Body)
	if string(body) != string(snap) {
		t.Fatalf("snapshot body mismatch: %q want %q", body, snap)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type=%q want application/octet-stream", got)
	}
}

func TestArtifactMemberAbsentReturns404MemberAbsent(t *testing.T) {
	s := memstore.New(memstore.WithClock(stepClock()))
	h := mustPutMember(t, s, []byte("src"), nil, nil)

	rec, req := newMemberRequest(string(h), string(store.MemberSnapshot))
	httpapi.ArtifactMember(s).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "member_absent" {
		t.Fatalf("reason=%q want member_absent", got)
	}
}

func TestArtifactMemberUnknownHashReturns404NotFound(t *testing.T) {
	rec, req := newMemberRequest("missing", string(store.MemberSource))
	httpapi.ArtifactMember(memstore.New()).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "not_found" {
		t.Fatalf("reason=%q want not_found", got)
	}
}

func TestArtifactMemberInvalidMemberKindReturns400(t *testing.T) {
	rec, req := newMemberRequest("abc", "rogue")
	httpapi.ArtifactMember(memstore.New()).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "invalid_member" {
		t.Fatalf("reason=%q want invalid_member", got)
	}
}

func TestArtifactMemberRejectsWrongMethod(t *testing.T) {
	rec, req := newMemberRequest("abc", string(store.MemberSource))
	req.Method = http.MethodDelete
	httpapi.ArtifactMember(memstore.New()).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}

func TestArtifactMemberPanicsAtConstructionOnNilReader(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	httpapi.ArtifactMember(nil)
}

// --- helpers ---

func mustPutMember(t *testing.T, s store.Store, src, snap, diag []byte) store.Hash {
	t.Helper()
	h, err := s.Put(context.Background(), store.PutRequest{
		SourceBytes:   src,
		SnapshotBytes: snap,
		DiagnoseBytes: diag,
		ContentType:   store.ContentTypeCSV,
		Metadata:      store.Metadata{CreatedBy: "ci-bot"},
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	return h
}

func newMemberRequest(hash, member string) (*httptest.ResponseRecorder, *http.Request) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/artifact/"+hash+"/"+member, nil)
	req.SetPathValue("hash", hash)
	req.SetPathValue("member", member)
	return rec, req
}

func decodeBundle(t *testing.T, r io.Reader) httpapi.ArtifactBundle {
	t.Helper()
	var b httpapi.ArtifactBundle
	if err := json.NewDecoder(r).Decode(&b); err != nil {
		t.Fatalf("decode bundle: %v", err)
	}
	return b
}
