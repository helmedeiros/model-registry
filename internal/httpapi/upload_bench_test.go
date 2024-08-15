package httpapi_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/helmedeiros/model-registry/internal/httpapi"
)

// BenchmarkPOST_Upload_SmallArtifact probes /upload against a 1 KB
// CSV — the size operators hit when iterating a small rule table.
// The multipart body is pre-built outside the loop so allocs/op
// reflects only the handler path, not the fixture.
// Pre-registered bar (ADR-0005 §191): < 200 ms / op.
func BenchmarkPOST_Upload_SmallArtifact(b *testing.B) {
	deps, _, _, _ := newUploadDeps(b)
	bodyBytes, ct := prebuildUploadMultipart(b, makeCSV(1<<10))
	handler := httpapi.Upload(deps)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(bodyBytes))
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("upload small: status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
}

// BenchmarkPOST_Upload_LargeArtifact probes /upload against a 2 MB
// CSV — the scale ADR-0005 §192 names. The multipart body is
// pre-built outside the loop.
// Pre-registered bar (ADR-0005 §192): < 1 s / op.
func BenchmarkPOST_Upload_LargeArtifact(b *testing.B) {
	deps, _, _, _ := newUploadDeps(b)
	bodyBytes, ct := prebuildUploadMultipart(b, makeCSV(2<<20))
	handler := httpapi.Upload(deps)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(bodyBytes))
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("upload large: status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
}

// prebuildUploadMultipart serialises the multipart envelope once;
// bench loops then cheap-clone via bytes.NewReader so the timed
// path measures only the handler.
func prebuildUploadMultipart(t testing.TB, source []byte) ([]byte, string) {
	parts := map[string]uploadPart{
		"source": {filename: "rules.csv", contentType: "text/csv", body: source},
	}
	r, ct := multipartBody(t, parts)
	bs, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("prebuildUploadMultipart: %v", err)
	}
	return bs, ct
}

// makeCSV builds a deterministic CSV body of approximately n bytes.
// One row per ~17 bytes; the loop pads so the body never undershoots
// the bench's nominal size.
func makeCSV(n int) []byte {
	const row = "alpha,rule,1.0,1\n"
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(row)
	}
	return []byte(b.String())
}
