package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/httpapi"
	"github.com/helmedeiros/model-registry/internal/store"
)

type stubBundleReader struct {
	bundles map[store.Hash]store.Bundle
}

func (s *stubBundleReader) GetBundle(_ context.Context, h store.Hash) (store.Bundle, error) {
	if b, ok := s.bundles[h]; ok {
		return b, nil
	}
	return store.Bundle{}, store.ErrNotFound
}

func (s *stubBundleReader) List(_ context.Context, _ store.ListOptions) (store.Page, error) {
	return store.Page{}, nil
}

func (s *stubBundleReader) GetMember(_ context.Context, _ store.Hash, _ store.MemberKind) ([]byte, store.ContentType, error) {
	return nil, store.ContentTypeUnknown, errors.New("not_supported")
}

func (s *stubBundleReader) ListTags(_ context.Context) (map[string]store.Hash, error) {
	return nil, nil
}

func (s *stubBundleReader) ResolveTag(_ context.Context, _ string) (store.Hash, error) {
	return "", errors.New("not_supported")
}

func TestDiffReportsAddedRemovedAndModifiedRules(t *testing.T) {
	bundleA := store.Bundle{
		Hash:        "aaa",
		ContentType: store.ContentTypeCSV,
		State:       store.StateActive,
		Metadata: store.Metadata{
			Rules: []store.RuleProvenance{
				{RuleID: "premium_uplift", Author: "alice", SourceCommitSHA: "abc1234"},
				{RuleID: "weekend_uplift", Author: "bob", SourceCommitSHA: "def5678"},
			},
		},
	}
	bundleB := store.Bundle{
		Hash:        "bbb",
		ContentType: store.ContentTypeCSV,
		State:       store.StateActive,
		Metadata: store.Metadata{
			Rules: []store.RuleProvenance{
				{RuleID: "premium_uplift", Author: "alice", SourceCommitSHA: "abc1234"}, // unchanged
				{RuleID: "loyalty_discount", Author: "carol", SourceCommitSHA: "999abcd"}, // added
				// weekend_uplift removed
			},
		},
	}
	reader := &stubBundleReader{bundles: map[store.Hash]store.Bundle{"aaa": bundleA, "bbb": bundleB}}

	srv := newDiffServer(reader)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/artifact/aaa/diff/bbb")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var got httpapi.ArtifactDiffView
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.From != "aaa" || got.To != "bbb" {
		t.Fatalf("hashes: %+v", got)
	}
	if len(got.Added) != 1 || got.Added[0].RuleID != "loyalty_discount" {
		t.Fatalf("added: %+v", got.Added)
	}
	if len(got.Removed) != 1 || got.Removed[0].RuleID != "weekend_uplift" {
		t.Fatalf("removed: %+v", got.Removed)
	}
	if len(got.Modified) != 0 {
		t.Fatalf("modified: %+v", got.Modified)
	}
}

func TestDiffDetectsRuleModification(t *testing.T) {
	bundleA := store.Bundle{Hash: "aaa", Metadata: store.Metadata{Rules: []store.RuleProvenance{
		{RuleID: "premium_uplift", Author: "alice", SourceCommitSHA: "abc1234", LastModified: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}}}
	bundleB := store.Bundle{Hash: "bbb", Metadata: store.Metadata{Rules: []store.RuleProvenance{
		{RuleID: "premium_uplift", Author: "alice", SourceCommitSHA: "def5678", LastModified: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)},
	}}}
	reader := &stubBundleReader{bundles: map[store.Hash]store.Bundle{"aaa": bundleA, "bbb": bundleB}}

	srv := newDiffServer(reader)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/artifact/aaa/diff/bbb")
	defer func() { _ = resp.Body.Close() }()
	var got httpapi.ArtifactDiffView
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if len(got.Modified) != 1 {
		t.Fatalf("modified: %+v", got.Modified)
	}
	if got.Modified[0].From.SourceCommitSHA != "abc1234" || got.Modified[0].To.SourceCommitSHA != "def5678" {
		t.Fatalf("modification not captured: %+v", got.Modified[0])
	}
}

func TestDiffReturns404OnUnknownHash(t *testing.T) {
	reader := &stubBundleReader{bundles: map[store.Hash]store.Bundle{}}
	srv := newDiffServer(reader)
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/artifact/aaa/diff/bbb")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func newDiffServer(r store.Reader) *httptest.Server {
	mux := http.NewServeMux()
	mux.Handle("/artifact/{from}/diff/{to}", httpapi.Diff(r))
	return httptest.NewServer(mux)
}
