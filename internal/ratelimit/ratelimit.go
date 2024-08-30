// Package ratelimit caps the per-env rate of write-surface calls so an
// operator (or a script) cannot thrash the data plane through rapid
// /promote + /rollback churn.
package ratelimit

import (
	"sync"
	"time"
)

type Limiter interface {
	Allow(key string) (ok bool, retryAfter time.Duration)
}

type NoopLimiter struct{}

func (NoopLimiter) Allow(string) (bool, time.Duration) { return true, 0 }

// TokenBucket is one bucket per key. Each bucket refills at rate `per`
// up to `burst` tokens. Allow consumes one token if available.
type TokenBucket struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	per     time.Duration
	burst   int
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

type Option func(*TokenBucket)

func WithClock(fn func() time.Time) Option { return func(t *TokenBucket) { t.now = fn } }

// NewTokenBucket caps each key at `burst` tokens, refilling one token
// every `per`. Example: NewTokenBucket(10*time.Second, 2) admits 2
// immediate calls + 1 every 10 s thereafter.
func NewTokenBucket(per time.Duration, burst int, opts ...Option) *TokenBucket {
	t := &TokenBucket{
		buckets: make(map[string]*bucket),
		per:     per,
		burst:   burst,
		now:     time.Now,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

func (t *TokenBucket) Allow(key string) (bool, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	b, ok := t.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(t.burst), last: now}
		t.buckets[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	if t.per > 0 {
		b.tokens += elapsed / t.per.Seconds()
		if b.tokens > float64(t.burst) {
			b.tokens = float64(t.burst)
		}
	}
	b.last = now
	if b.tokens < 1 {
		need := 1 - b.tokens
		return false, time.Duration(need * float64(t.per))
	}
	b.tokens--
	return true, 0
}
