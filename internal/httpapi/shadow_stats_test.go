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
	"github.com/helmedeiros/model-registry/internal/shadowstats"
)

type stubShadowReader struct {
	stats shadowstats.Stats
	err   error
}

func (s stubShadowReader) Stats(_ context.Context, _ time.Duration) (shadowstats.Stats, error) {
	return s.stats, s.err
}

func TestShadowStatsReturnsAggregatedView(t *testing.T) {
	r := stubShadowReader{stats: shadowstats.Stats{
		Since:            5 * time.Minute,
		AgreementRate:    0.97,
		AgreementSamples: 15000,
		FactorDeltaP99:   0.04,
	}}
	mux := http.NewServeMux()
	mux.Handle("/shadow-stats", httpapi.ShadowStats(httpapi.ShadowStatsDeps{Reader: r}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/shadow-stats")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var got httpapi.ShadowStatsView
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.AgreementRate != 0.97 || got.AgreementSamples != 15000 {
		t.Fatalf("body: %+v", got)
	}
}

func TestShadowStatsReturns503WhenReaderDisabled(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/shadow-stats", httpapi.ShadowStats(httpapi.ShadowStatsDeps{Reader: stubShadowReader{err: shadowstats.ErrDisabled}}))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/shadow-stats")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", resp.StatusCode)
	}
}

func TestShadowStatsReturns502OnUpstreamError(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/shadow-stats", httpapi.ShadowStats(httpapi.ShadowStatsDeps{Reader: stubShadowReader{err: errors.New("prom down")}}))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/shadow-stats")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status=%d want 502", resp.StatusCode)
	}
}
