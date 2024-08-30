package businessstats_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/businessstats"
)

type stubRoute struct {
	match  string
	result any
}

func promStub(routes []stubRoute) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		for _, route := range routes {
			if strings.Contains(q, route.match) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": "success",
					"data":   map[string]any{"result": route.result},
				})
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   map[string]any{"result": []any{}},
		})
	}))
}

func scalar(v float64) []map[string]any {
	return []map[string]any{{
		"metric": map[string]string{},
		"value":  []any{1.0, strconv.FormatFloat(v, 'f', -1, 64)},
	}}
}

func TestPromReaderAggregatesOutcomeRPSAndFactorPercentiles(t *testing.T) {
	srv := promStub([]stubRoute{
		{`topk`, []map[string]any{
			{"metric": map[string]string{"rule": "premium_uplift"}, "value": []any{1.0, "60"}},
			{"metric": map[string]string{"rule": "loyalty_discount"}, "value": []any{1.0, "40"}},
		}},
		{`histogram_quantile(0.50`, scalar(1.2)},
		{`histogram_quantile(0.95`, scalar(1.8)},
		{`histogram_quantile(0.99`, scalar(2.4)},
		{`outcome="ok"`, scalar(100)},
		{`outcome="error"`, scalar(2)},
		{`outcome="no_match"`, scalar(5)},
		{`markup_decide_total{env=`, scalar(107)},
	})
	defer srv.Close()

	r := businessstats.NewPromReader(srv.URL, businessstats.WithPromClient(srv.Client()))
	got, err := r.Stats(context.Background(), "production", 5*time.Minute)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if got.DecideRPS.OK != 100 || got.DecideRPS.Total != 107 {
		t.Fatalf("DecideRPS: %+v", got.DecideRPS)
	}
	if got.FactorP50 != 1.2 || got.FactorP99 != 2.4 {
		t.Fatalf("factor percentiles: %+v", got)
	}
	if len(got.TopRules) != 2 || got.TopRules[0].Rule != "premium_uplift" || got.TopRules[0].RatePerSecond != 60 {
		t.Fatalf("top rules: %+v", got.TopRules)
	}
}

func TestPromReaderEmitsSingleUnitPromQLDuration(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Query().Get("query"))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"result": []any{}}})
	}))
	defer srv.Close()
	r := businessstats.NewPromReader(srv.URL, businessstats.WithPromClient(srv.Client()))
	_, err := r.Stats(context.Background(), "production", 5*time.Minute)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, q := range seen {
		if strings.Contains(q, "m0s") || strings.Contains(q, "h0m") {
			t.Fatalf("compound PromQL duration leaked: %s", q)
		}
		if !strings.Contains(q, "[300s]") {
			t.Fatalf("expected [300s] in query, got: %s", q)
		}
	}
}

func TestPromReaderReturnsErrorOnUpstreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	r := businessstats.NewPromReader(srv.URL, businessstats.WithPromClient(srv.Client()))
	_, err := r.Stats(context.Background(), "production", 5*time.Minute)
	if err == nil {
		t.Fatal("expected error")
	}
}
