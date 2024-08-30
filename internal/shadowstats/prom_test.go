package shadowstats_test

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

	"github.com/helmedeiros/model-registry/internal/shadowstats"
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
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"result": []any{}}})
	}))
}

func scalar(v float64) []map[string]any {
	return []map[string]any{{
		"metric": map[string]string{},
		"value":  []any{1.0, strconv.FormatFloat(v, 'f', -1, 64)},
	}}
}

func TestPromReaderAggregatesShadowMetrics(t *testing.T) {
	srv := promStub([]stubRoute{
		{`agree="true"`, scalar(95)},
		{`agree="false"`, scalar(5)},
		{`increase(markup_challenger_agreement_total`, scalar(30000)},
		{`side="champion_only"`, scalar(0.5)},
		{`side="challenger_only"`, scalar(0.3)},
		{`eval_timeout_total`, scalar(0.1)},
		{`eval_errors_total`, scalar(0.02)},
		{`histogram_quantile(0.50`, scalar(0.005)},
		{`histogram_quantile(0.95`, scalar(0.03)},
		{`histogram_quantile(0.99`, scalar(0.08)},
	})
	defer srv.Close()
	r := shadowstats.NewPromReader(srv.URL, shadowstats.WithPromClient(srv.Client()))
	got, err := r.Stats(context.Background(), 5*time.Minute)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if got.AgreementRate != 0.95 {
		t.Fatalf("agreement_rate=%v want 0.95", got.AgreementRate)
	}
	if got.AgreementSamples != 30000 {
		t.Fatalf("samples=%v want 30000", got.AgreementSamples)
	}
	if got.FactorDeltaP99 != 0.08 {
		t.Fatalf("p99=%v want 0.08", got.FactorDeltaP99)
	}
}

func TestPromReaderReturnsErrorOnUpstreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	r := shadowstats.NewPromReader(srv.URL, shadowstats.WithPromClient(srv.Client()))
	_, err := r.Stats(context.Background(), 5*time.Minute)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPromReaderSurfacesUnparseableValue(t *testing.T) {
	srv := promStub([]stubRoute{
		{`histogram_quantile(0.99`, []map[string]any{{"metric": map[string]string{}, "value": []any{1.0, "not-a-number"}}}},
	})
	defer srv.Close()
	r := shadowstats.NewPromReader(srv.URL, shadowstats.WithPromClient(srv.Client()))
	_, err := r.Stats(context.Background(), 5*time.Minute)
	if err == nil {
		t.Fatal("expected parse error to surface; got nil (would silently return p99=0 and gate falsely green)")
	}
}

func TestPromReaderEmitsSingleUnitDuration(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Query().Get("query"))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"result": []any{}}})
	}))
	defer srv.Close()
	r := shadowstats.NewPromReader(srv.URL, shadowstats.WithPromClient(srv.Client()))
	_, err := r.Stats(context.Background(), 5*time.Minute)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, q := range seen {
		if strings.Contains(q, "m0s") || strings.Contains(q, "h0m") {
			t.Fatalf("compound PromQL duration leaked: %s", q)
		}
	}
}
