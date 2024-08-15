package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/audit/memaudit"
	"github.com/helmedeiros/model-registry/internal/httpapi"
	"github.com/helmedeiros/model-registry/internal/store"
	"github.com/helmedeiros/model-registry/internal/store/memstore"
)

type stubULID struct {
	next int
}

func (s *stubULID) New() (string, error) {
	s.next++
	return "01HXYTEST" + string(rune('A'+s.next-1)), nil
}

type captureSink struct {
	msg   string
	attrs map[string]any
}

func (c *captureSink) Info(msg string, attrs map[string]any) {
	c.msg = msg
	c.attrs = attrs
}

func newUploadDeps(t testing.TB) (httpapi.UploadDeps, store.Store, audit.Reader, *captureSink) {
	t.Helper()
	st := memstore.New()
	au := memaudit.New()
	sink := &captureSink{}
	return httpapi.UploadDeps{
		Substrate: st,
		Audit:     au,
		ULID:      &stubULID{},
		Logger:    sink,
		Now:       func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}, st, au, sink
}

func multipartBody(t testing.TB, parts map[string]uploadPart) (io.Reader, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)
	for name, part := range parts {
		hdr := textproto.MIMEHeader{}
		hdr.Set("Content-Disposition", `form-data; name="`+name+`"; filename="`+part.filename+`"`)
		hdr.Set("Content-Type", part.contentType)
		fw, err := w.CreatePart(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(part.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf, w.FormDataContentType()
}

type uploadPart struct {
	filename    string
	contentType string
	body        []byte
}

func TestUploadHappyPathStoresArtifactAndRecordsAudit(t *testing.T) {
	deps, st, au, _ := newUploadDeps(t)
	body, ct := multipartBody(t, map[string]uploadPart{
		"source":   {filename: "rules.csv", contentType: "text/csv", body: []byte("alpha,rule,1.0,1\n")},
		"metadata": {filename: "metadata.json", contentType: "application/json", body: []byte(`{"created_by":"alice","description":"first cut"}`)},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", ct)
	httpapi.Upload(deps).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp httpapi.UploadResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Hash == "" || resp.State != "staged" {
		t.Fatalf("response: %+v", resp)
	}

	bundle, err := st.GetBundle(context.Background(), store.Hash(resp.Hash))
	if err != nil {
		t.Fatalf("GetBundle: %v", err)
	}
	if bundle.Metadata.CreatedBy != "alice" || bundle.Metadata.Description != "first cut" {
		t.Fatalf("metadata not persisted: %+v", bundle.Metadata)
	}

	page, err := au.List(context.Background(), audit.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Action != "upload" || page.Items[0].Operator != "alice" {
		t.Fatalf("audit not recorded: %+v", page)
	}
	if page.Items[0].ArtifactHash != store.Hash(resp.Hash) {
		t.Fatalf("audit hash=%q want %q", page.Items[0].ArtifactHash, resp.Hash)
	}
}

func TestUploadMissingSourcePartReturns400(t *testing.T) {
	deps, _, _, _ := newUploadDeps(t)
	body, ct := multipartBody(t, map[string]uploadPart{
		"metadata": {filename: "metadata.json", contentType: "application/json", body: []byte(`{"created_by":"alice"}`)},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", ct)
	httpapi.Upload(deps).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "source_required" {
		t.Fatalf("reason=%q want source_required", got)
	}
}

func TestUploadUnsupportedContentTypeReturns400(t *testing.T) {
	deps, _, _, _ := newUploadDeps(t)
	body, ct := multipartBody(t, map[string]uploadPart{
		"source": {filename: "rules.bin", contentType: "application/octet-stream", body: []byte("blob")},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", ct)
	httpapi.Upload(deps).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "unsupported_content_type" {
		t.Fatalf("reason=%q want unsupported_content_type", got)
	}
}

func TestUploadRejectsOverSizeSource(t *testing.T) {
	deps, _, _, _ := newUploadDeps(t)
	deps.MaxBytes = 32
	body, ct := multipartBody(t, map[string]uploadPart{
		"source": {filename: "rules.csv", contentType: "text/csv", body: bytes.Repeat([]byte("A"), 1024)},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", ct)
	httpapi.Upload(deps).ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d want 413", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "upload_too_large" {
		t.Fatalf("reason=%q want upload_too_large", got)
	}
}

func TestUploadInvalidMetadataJSONReturns400(t *testing.T) {
	deps, _, _, _ := newUploadDeps(t)
	body, ct := multipartBody(t, map[string]uploadPart{
		"source":   {filename: "rules.csv", contentType: "text/csv", body: []byte("alpha,rule,1.0,1\n")},
		"metadata": {filename: "metadata.json", contentType: "application/json", body: []byte("not json")},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", ct)
	httpapi.Upload(deps).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestUploadIsIdempotentOnSameSourceBytes(t *testing.T) {
	deps, _, au, _ := newUploadDeps(t)
	source := []byte("alpha,rule,1.0,1\n")

	for i := 0; i < 2; i++ {
		body, ct := multipartBody(t, map[string]uploadPart{
			"source":   {filename: "rules.csv", contentType: "text/csv", body: source},
			"metadata": {filename: "metadata.json", contentType: "application/json", body: []byte(`{"created_by":"alice"}`)},
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/upload", body)
		req.Header.Set("Content-Type", ct)
		httpapi.Upload(deps).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d: status=%d body=%s", i, rec.Code, rec.Body.String())
		}
	}

	page, _ := au.List(context.Background(), audit.ListOptions{})
	if len(page.Items) != 2 {
		t.Fatalf("expected 2 audit entries (one per attempt), got %d", len(page.Items))
	}
}

type failingAuditWriter struct{}

func (failingAuditWriter) Record(_ context.Context, _ audit.Entry) error {
	return errors.New("synthetic audit failure")
}

func TestUploadStillReturns200WhenAuditWriteFailsButLogsTheGap(t *testing.T) {
	deps, _, _, sink := newUploadDeps(t)
	deps.Audit = failingAuditWriter{}

	body, ct := multipartBody(t, map[string]uploadPart{
		"source":   {filename: "rules.csv", contentType: "text/csv", body: []byte("alpha,rule,1.0,1\n")},
		"metadata": {filename: "metadata.json", contentType: "application/json", body: []byte(`{"created_by":"alice"}`)},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", ct)
	httpapi.Upload(deps).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 — put already committed; failing audit must not mask that", rec.Code)
	}
	if sink.msg != "registry.audit.write_failed" {
		t.Fatalf("logger did not see audit-failure event: %q", sink.msg)
	}
	if sink.attrs["action"] != "upload" {
		t.Fatalf("audit-failure event missing action attr: %+v", sink.attrs)
	}
}

func TestUploadRejectsWrongMethod(t *testing.T) {
	deps, _, _, _ := newUploadDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/upload", nil)
	httpapi.Upload(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}

func TestUploadPanicsAtConstructionOnMissingDeps(t *testing.T) {
	for _, tc := range []struct {
		name string
		deps httpapi.UploadDeps
	}{
		{"nil substrate", httpapi.UploadDeps{Audit: memaudit.New(), ULID: &stubULID{}, Logger: &captureSink{}}},
		{"nil audit", httpapi.UploadDeps{Substrate: memstore.New(), ULID: &stubULID{}, Logger: &captureSink{}}},
		{"nil ulid", httpapi.UploadDeps{Substrate: memstore.New(), Audit: memaudit.New(), Logger: &captureSink{}}},
		{"nil logger", httpapi.UploadDeps{Substrate: memstore.New(), Audit: memaudit.New(), ULID: &stubULID{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic")
				}
			}()
			httpapi.Upload(tc.deps)
		})
	}
}
