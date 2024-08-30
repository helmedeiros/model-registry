package ratelimit_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/ratelimit"
)

type fakeClock struct{ t atomic.Int64 }

func (c *fakeClock) Now() time.Time          { return time.Unix(0, c.t.Load()) }
func (c *fakeClock) Advance(d time.Duration) { c.t.Add(int64(d)) }

func TestTokenBucketAllowsBurstThenThrottles(t *testing.T) {
	clk := &fakeClock{}
	clk.Advance(time.Second)
	tb := ratelimit.NewTokenBucket(10*time.Second, 2, ratelimit.WithClock(clk.Now))
	for i := 0; i < 2; i++ {
		ok, _ := tb.Allow("production")
		if !ok {
			t.Fatalf("burst[%d] should be allowed", i)
		}
	}
	ok, retry := tb.Allow("production")
	if ok {
		t.Fatal("3rd call within burst should be denied")
	}
	if retry <= 0 || retry > 10*time.Second {
		t.Fatalf("retry=%v want >0 and <=10s", retry)
	}
}

func TestTokenBucketRefillsAfterPer(t *testing.T) {
	clk := &fakeClock{}
	clk.Advance(time.Second)
	tb := ratelimit.NewTokenBucket(5*time.Second, 1, ratelimit.WithClock(clk.Now))
	if ok, _ := tb.Allow("production"); !ok {
		t.Fatal("first should pass")
	}
	if ok, _ := tb.Allow("production"); ok {
		t.Fatal("second should be denied")
	}
	clk.Advance(5 * time.Second)
	if ok, _ := tb.Allow("production"); !ok {
		t.Fatal("after refill should pass")
	}
}

func TestTokenBucketIsPerKey(t *testing.T) {
	clk := &fakeClock{}
	clk.Advance(time.Second)
	tb := ratelimit.NewTokenBucket(10*time.Second, 1, ratelimit.WithClock(clk.Now))
	if ok, _ := tb.Allow("production"); !ok {
		t.Fatal("first production should pass")
	}
	if ok, _ := tb.Allow("staging"); !ok {
		t.Fatal("first staging should pass independently of production")
	}
	if ok, _ := tb.Allow("production"); ok {
		t.Fatal("second production should be denied")
	}
}

func TestNoopLimiterAlwaysAllows(t *testing.T) {
	for i := 0; i < 100; i++ {
		if ok, _ := (ratelimit.NoopLimiter{}).Allow("production"); !ok {
			t.Fatal("noop must always allow")
		}
	}
}
