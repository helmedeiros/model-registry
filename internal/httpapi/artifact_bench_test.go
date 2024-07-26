package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/helmedeiros/model-registry/internal/httpapi"
	"github.com/helmedeiros/model-registry/internal/store"
	"github.com/helmedeiros/model-registry/internal/store/memstore"
)

// BenchmarkGET_Artifact_ByHash pins the per-request cost of the
// /artifact/{hash} handler against ADR-0004's < 5 ms bar. The bench
// reads the substrate cost + JSON marshal on the memstore path
// (allocation-honest baseline; fsstore wall-clock is captured by the
// substrate bench separately).
func BenchmarkGET_Artifact_ByHash(b *testing.B) {
	s := memstore.New()
	h, err := s.Put(context.Background(), store.PutRequest{
		SourceBytes: []byte("alpha,rule,1.0,1\n"),
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedBy: "bench"},
	})
	if err != nil {
		b.Fatalf("Put: %v", err)
	}

	handler := httpapi.Artifact(s)
	req := httptest.NewRequest(http.MethodGet, "/artifact/"+string(h), nil)
	req.SetPathValue("hash", string(h))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}

// BenchmarkGET_ArtifactMember_Source10KB pins the source-member byte
// stream cost. ADR-0004 commits a < 50 ms wall bar on /artifacts; the
// member endpoint inherits the same chain plus the
// substrate-clone-source-slice cost ADR-0002 documents.
func BenchmarkGET_ArtifactMember_Source10KB(b *testing.B) {
	s := memstore.New()
	src := make([]byte, 10*1024)
	for i := range src {
		src[i] = byte(i)
	}
	h, err := s.Put(context.Background(), store.PutRequest{
		SourceBytes: src,
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedBy: "bench"},
	})
	if err != nil {
		b.Fatalf("Put: %v", err)
	}

	handler := httpapi.ArtifactMember(s)
	req := httptest.NewRequest(http.MethodGet, "/artifact/"+string(h)+"/source", nil)
	req.SetPathValue("hash", string(h))
	req.SetPathValue("member", string(store.MemberSource))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}
