package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/httpapi"
	"github.com/helmedeiros/model-registry/internal/ratelimit"
)

type fakeClock struct{ t atomic.Int64 }

func (c *fakeClock) Now() time.Time          { return time.Unix(0, c.t.Load()) }
func (c *fakeClock) Advance(d time.Duration) { c.t.Add(int64(d)) }

func TestPromoteRateLimitedReturns429WithRetryAfter(t *testing.T) {
	clk := &fakeClock{}
	clk.Advance(time.Second)
	// burst=1, per=60s — freshly-exhausted bucket means exactly 60 s
	// to the next token. Asserting the exact integer guards against a
	// regression of the floor+1 / ceil bug.
	limiter := ratelimit.NewTokenBucket(60*time.Second, 1, ratelimit.WithClock(clk.Now))
	deps, st, _, _, _ := newPromoteDeps(t, okResult("http://markup-svc-1:8080"))
	deps.Limiter = limiter
	h := putRule(t, st, []byte("alpha,rule,1.0,1\n"))

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/promote", promoteBody(t, httpapi.PromoteRequest{
			Hash: string(h), Env: "production", Role: "champion", Operator: "alice", Reason: "rapid",
		}))
		httpapi.Promote(deps).ServeHTTP(rec, req)
		switch i {
		case 0:
			if rec.Code != http.StatusOK {
				t.Fatalf("first call: status=%d body=%s", rec.Code, rec.Body.String())
			}
		case 1:
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("second call: status=%d want 429", rec.Code)
			}
			retry := rec.Header().Get("Retry-After")
			n, err := strconv.Atoi(retry)
			if err != nil {
				t.Fatalf("Retry-After=%q invalid", retry)
			}
			if n != 60 {
				t.Fatalf("Retry-After=%d want exactly 60 (per=60s, burst=1, freshly-exhausted)", n)
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["reason"] != "promote_rate_limited" {
				t.Fatalf("reason=%q want promote_rate_limited", body["reason"])
			}
		}
	}
}

func TestRollbackRateLimitedReturns429(t *testing.T) {
	clk := &fakeClock{}
	clk.Advance(time.Second)
	limiter := ratelimit.NewTokenBucket(60*time.Second, 1, ratelimit.WithClock(clk.Now))
	deps, st, envState, _, _ := newRollbackDeps(t, okResult("http://markup-svc-1:8080"))
	deps.Limiter = limiter
	h1 := putRule(t, st, []byte("a,rule,1.0,1\n"))
	h2 := putRule(t, st, []byte("b,rule,1.0,1\n"))
	if _, err := envState.PromoteChampion(context.Background(), "production", h1, "ci-bot", "seed"); err != nil {
		t.Fatal(err)
	}
	if _, err := envState.PromoteChampion(context.Background(), "production", h2, "ci-bot", "seed"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/rollback", bytes.NewReader(rollbackBodyBytes(t, "production", "alice", "first")))
	httpapi.Rollback(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first /rollback: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/rollback", bytes.NewReader(rollbackBodyBytes(t, "production", "alice", "second")))
	httpapi.Rollback(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second /rollback: %d want 429", rec.Code)
	}
	n, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || n != 60 {
		t.Fatalf("Retry-After=%q want 60", rec.Header().Get("Retry-After"))
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["reason"] != "rollback_rate_limited" {
		t.Fatalf("reason=%q want rollback_rate_limited", body["reason"])
	}
}

func rollbackBodyBytes(t *testing.T, env, op, reason string) []byte {
	t.Helper()
	b, err := json.Marshal(httpapi.RollbackRequest{Env: env, Operator: op, Reason: reason})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
