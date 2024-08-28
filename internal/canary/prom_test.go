package canary_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/canary"
)

func promStub(errVal, totalVal float64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		val := totalVal
		if strings.Contains(q, `outcome="error"`) {
			val = errVal
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"result": []map[string]any{
					{"value": []any{1.0, formatFloat(val)}},
				},
			},
		})
	}))
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

type stepClock struct {
	t atomic.Int64
}

func (c *stepClock) Now() time.Time { return time.Unix(c.t.Load(), 0) }
func (c *stepClock) Advance(d time.Duration) {
	c.t.Add(int64(d.Seconds()))
}

func TestPromDeciderKeptWhenErrorRateBelowThreshold(t *testing.T) {
	srv := promStub(1, 1000)
	defer srv.Close()
	clk := &stepClock{}
	clk.Advance(1)

	d := canary.NewPromDecider(srv.URL,
		canary.WithPromClient(srv.Client()),
		canary.WithPromWindow(2*time.Second),
		canary.WithPromPollEvery(time.Millisecond),
		canary.WithPromThreshold(0.05),
		canary.WithPromMinSamples(10),
		canary.WithPromClock(clk.Now),
	)
	go func() {
		time.Sleep(10 * time.Millisecond)
		clk.Advance(3 * time.Second)
	}()
	dec, obs, err := d.Decide(context.Background(), "production")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec != canary.DecisionKept {
		t.Fatalf("decision=%q want kept; obs=%+v", dec, obs)
	}
	if obs.SampleCount != 1000 || obs.ErrorRate != 0.001 {
		t.Fatalf("observation: %+v", obs)
	}
}

func TestPromDeciderRolledBackWhenErrorRateAboveThreshold(t *testing.T) {
	srv := promStub(50, 1000)
	defer srv.Close()
	clk := &stepClock{}
	clk.Advance(1)

	d := canary.NewPromDecider(srv.URL,
		canary.WithPromClient(srv.Client()),
		canary.WithPromWindow(time.Hour),
		canary.WithPromPollEvery(time.Millisecond),
		canary.WithPromThreshold(0.01),
		canary.WithPromMinSamples(100),
		canary.WithPromClock(clk.Now),
	)
	dec, obs, err := d.Decide(context.Background(), "production")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec != canary.DecisionRolledBack {
		t.Fatalf("decision=%q want rolled_back; obs=%+v", dec, obs)
	}
	if obs.ErrorRate != 0.05 {
		t.Fatalf("error_rate=%v want 0.05", obs.ErrorRate)
	}
}

func TestPromDeciderInconclusiveBelowMinSamples(t *testing.T) {
	srv := promStub(0, 5)
	defer srv.Close()
	clk := &stepClock{}
	clk.Advance(1)

	d := canary.NewPromDecider(srv.URL,
		canary.WithPromClient(srv.Client()),
		canary.WithPromWindow(2*time.Second),
		canary.WithPromPollEvery(time.Millisecond),
		canary.WithPromMinSamples(100),
		canary.WithPromClock(clk.Now),
	)
	go func() {
		time.Sleep(10 * time.Millisecond)
		clk.Advance(3 * time.Second)
	}()
	dec, _, err := d.Decide(context.Background(), "production")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec != canary.DecisionInconclusive {
		t.Fatalf("decision=%q want inconclusive", dec)
	}
}

func TestPromDeciderUpstreamErrorReturnsErrUpstreamUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	d := canary.NewPromDecider(srv.URL,
		canary.WithPromClient(srv.Client()),
		canary.WithPromWindow(2*time.Second),
		canary.WithPromPollEvery(time.Millisecond),
	)
	_, _, err := d.Decide(context.Background(), "production")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "upstream metric store unreachable") {
		t.Fatalf("err=%v want ErrUpstreamUnreachable wrap", err)
	}
}
