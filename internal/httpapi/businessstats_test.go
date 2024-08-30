package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/businessstats"
	"github.com/helmedeiros/model-registry/internal/httpapi"
)

type stubReader struct {
	stats businessstats.Stats
	err   error
	gotEnv string
	gotSince time.Duration
}

func (s *stubReader) Stats(_ context.Context, env string, since time.Duration) (businessstats.Stats, error) {
	s.gotEnv = env
	s.gotSince = since
	return s.stats, s.err
}

func newServer(r businessstats.Reader) *httptest.Server {
	mux := http.NewServeMux()
	mux.Handle("/env/{env}/business-stats", httpapi.BusinessStats(httpapi.BusinessStatsDeps{Reader: r}))
	return httptest.NewServer(mux)
}

func TestBusinessStatsReturnsAggregatedView(t *testing.T) {
	r := &stubReader{stats: businessstats.Stats{
		Env:       "production",
		Since:     5 * time.Minute,
		DecideRPS: businessstats.OutcomeRPS{OK: 100, Total: 107},
		FactorP50: 1.2,
		TopRules:  []businessstats.RuleHit{{Rule: "premium_uplift", RatePerSecond: 60}},
	}}
	srv := newServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/env/production/business-stats")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var got httpapi.BusinessStatsView
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Env != "production" || got.Decide.OK != 100 || got.Factor.P50 != 1.2 {
		t.Fatalf("body: %+v", got)
	}
	if r.gotSince != 5*time.Minute {
		t.Fatalf("default since: %v", r.gotSince)
	}
}

func TestBusinessStatsRespectsSinceQueryParam(t *testing.T) {
	r := &stubReader{}
	srv := newServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/env/production/business-stats?since=15m")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if r.gotSince != 15*time.Minute {
		t.Fatalf("since: %v", r.gotSince)
	}
}

func TestBusinessStatsRejectsOutOfRangeSince(t *testing.T) {
	srv := newServer(&stubReader{})
	defer srv.Close()
	for _, tc := range []struct{ q string }{{"since=10s"}, {"since=48h"}, {"since=banana"}} {
		resp, _ := http.Get(srv.URL + "/env/production/business-stats?" + tc.q)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d", tc.q, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

func TestBusinessStatsReturns503WhenReaderDisabled(t *testing.T) {
	srv := newServer(&stubReader{err: businessstats.ErrDisabled})
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/env/production/business-stats")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestBusinessStatsReturns502OnUpstreamError(t *testing.T) {
	srv := newServer(&stubReader{err: errors.New("prom down")})
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/env/production/business-stats")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := readAll(resp.Body)
	if !strings.Contains(string(body), "business_stats_upstream") {
		t.Fatalf("body did not carry opaque reason: %s", body)
	}
}
