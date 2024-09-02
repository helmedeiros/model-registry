package autopromote_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/autopromote"
	"github.com/helmedeiros/model-registry/internal/envstate/memstate"
	"github.com/helmedeiros/model-registry/internal/shadowstats"
)

type stubShadow struct {
	stats shadowstats.Stats
	err   error
}

func (s stubShadow) Stats(_ context.Context, _ time.Duration) (shadowstats.Stats, error) {
	return s.stats, s.err
}

type captureLogger struct {
	mu     sync.Mutex
	events []event
}

type event struct {
	msg   string
	attrs map[string]any
}

func (c *captureLogger) Info(msg string, attrs map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event{msg: msg, attrs: attrs})
}

func (c *captureLogger) seen(msg string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.msg == msg {
			return true
		}
	}
	return false
}

func (c *captureLogger) count(msg string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.events {
		if e.msg == msg {
			n++
		}
	}
	return n
}

func (c *captureLogger) findAttrs(msg string) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.msg == msg {
			return e.attrs
		}
	}
	return nil
}

func TestObserver_GateClearedAfterConsecutiveTicksLogsRecommendation(t *testing.T) {
	envState := memstate.New()
	if err := envState.PromoteChallenger(context.Background(), "production", "h-abc123", "alice", "shadow trial"); err != nil {
		t.Fatal(err)
	}
	stats := shadowstats.Stats{
		AgreementRate:    0.999,
		AgreementSamples: 15000,
		FactorDeltaP99:   0.02,
	}
	log := &captureLogger{}
	obs := autopromote.New(autopromote.Config{
		Envs:             []string{"production"},
		EnvState:         envState,
		Shadow:           stubShadow{stats: stats},
		Logger:           log,
		Interval:         5 * time.Millisecond,
		ConsecutiveTicks: 2,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go obs.Start(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if log.seen("registry.autopromote.gate_cleared") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if !log.seen("registry.autopromote.gate_cleared") {
		t.Fatalf("gate_cleared event never logged")
	}
	attrs := log.findAttrs("registry.autopromote.gate_cleared")
	if attrs["mode"] != "log_only" {
		t.Fatalf("mode=%v want log_only", attrs["mode"])
	}
	if attrs["challenger_hash"] != "h-abc123" {
		t.Fatalf("challenger_hash=%v", attrs["challenger_hash"])
	}
}

func TestObserver_GateBelowThresholdDoesNotLog(t *testing.T) {
	envState := memstate.New()
	if err := envState.PromoteChallenger(context.Background(), "production", "h-bad", "alice", "shadow trial"); err != nil {
		t.Fatal(err)
	}
	stats := shadowstats.Stats{
		AgreementRate:    0.7, // below default 0.99
		AgreementSamples: 15000,
		FactorDeltaP99:   0.02,
	}
	log := &captureLogger{}
	obs := autopromote.New(autopromote.Config{
		Envs:             []string{"production"},
		EnvState:         envState,
		Shadow:           stubShadow{stats: stats},
		Logger:           log,
		Interval:         5 * time.Millisecond,
		ConsecutiveTicks: 1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go obs.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()
	if log.seen("registry.autopromote.gate_cleared") {
		t.Fatal("gate_cleared logged even though agreement below threshold")
	}
}

func TestObserver_NoChallengerEnvSkipped(t *testing.T) {
	envState := memstate.New() // no challenger
	log := &captureLogger{}
	obs := autopromote.New(autopromote.Config{
		Envs:             []string{"production"},
		EnvState:         envState,
		Shadow:           stubShadow{stats: shadowstats.Stats{AgreementRate: 0.999, AgreementSamples: 15000}},
		Logger:           log,
		Interval:         5 * time.Millisecond,
		ConsecutiveTicks: 1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go obs.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()
	if log.seen("registry.autopromote.gate_cleared") {
		t.Fatal("gate_cleared logged for env without a challenger")
	}
}

func TestObserver_GateStaysClearReNotifiesEveryTick(t *testing.T) {
	envState := memstate.New()
	if err := envState.PromoteChallenger(context.Background(), "production", "h-re", "alice", "shadow"); err != nil {
		t.Fatal(err)
	}
	stats := shadowstats.Stats{AgreementRate: 0.999, AgreementSamples: 15000, FactorDeltaP99: 0.02}
	log := &captureLogger{}
	obs := autopromote.New(autopromote.Config{
		Envs:             []string{"production"},
		EnvState:         envState,
		Shadow:           stubShadow{stats: stats},
		Logger:           log,
		Interval:         5 * time.Millisecond,
		ConsecutiveTicks: 2,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go obs.Start(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if log.count("registry.autopromote.gate_cleared") >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if got := log.count("registry.autopromote.gate_cleared"); got < 2 {
		t.Fatalf("gate_cleared count=%d, want >=2 (re-notification expected on every tick while gate stays clear)", got)
	}
}

func TestObserver_StatsErrorResetsStreak(t *testing.T) {
	envState := memstate.New()
	if err := envState.PromoteChallenger(context.Background(), "production", "h-reset", "alice", "shadow"); err != nil {
		t.Fatal(err)
	}
	clear := shadowstats.Stats{AgreementRate: 0.999, AgreementSamples: 15000, FactorDeltaP99: 0.02}
	shadow := &toggleShadow{stats: clear}
	log := &captureLogger{}
	obs := autopromote.New(autopromote.Config{
		Envs:             []string{"production"},
		EnvState:         envState,
		Shadow:           shadow,
		Logger:           log,
		Interval:         5 * time.Millisecond,
		ConsecutiveTicks: 3,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go obs.Start(ctx)
	// Drive 2 clearing ticks then flip Stats to error.
	time.Sleep(20 * time.Millisecond)
	shadow.setErr(errors.New("prom down"))
	time.Sleep(20 * time.Millisecond)
	// Restore clearing stats; streak must rebuild from zero, so 3 more
	// ticks are needed before gate_cleared logs.
	shadow.setErr(nil)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if log.seen("registry.autopromote.gate_cleared") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if !log.seen("registry.autopromote.stats_unavailable") {
		t.Fatal("stats_unavailable should have logged during the outage window")
	}
	if !log.seen("registry.autopromote.gate_cleared") {
		t.Fatal("gate_cleared should re-fire after outage once streak rebuilds")
	}
}

type toggleShadow struct {
	mu    sync.Mutex
	stats shadowstats.Stats
	err   error
}

func (s *toggleShadow) Stats(_ context.Context, _ time.Duration) (shadowstats.Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats, s.err
}

func (s *toggleShadow) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func TestObserver_ShadowStatsUnavailableLogsButContinues(t *testing.T) {
	envState := memstate.New()
	if err := envState.PromoteChallenger(context.Background(), "production", "h-x", "alice", "shadow"); err != nil {
		t.Fatal(err)
	}
	log := &captureLogger{}
	obs := autopromote.New(autopromote.Config{
		Envs:             []string{"production"},
		EnvState:         envState,
		Shadow:           stubShadow{err: errors.New("prom down")},
		Logger:           log,
		Interval:         5 * time.Millisecond,
		ConsecutiveTicks: 1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go obs.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()
	if !log.seen("registry.autopromote.stats_unavailable") {
		t.Fatal("stats_unavailable should have logged on prom error")
	}
}
