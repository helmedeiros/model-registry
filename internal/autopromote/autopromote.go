// Package autopromote watches the shadow-Decider comparison metrics
// and tells the operator when the promote-to-champion gate has
// cleared. This MVP is log-only: it does NOT call PromoteChampion.
// The operator reads `registry.autopromote.gate_cleared` and runs
// `mrctl promote --role champion --hash <challenger_hash>`.
//
// Gate thresholds (ADR-0013):
//
//	agreement_rate >= MinAgreement (default 0.99)
//	agreement_samples >= MinSamples (default 10_000)
//	factor_delta_p99 <= MaxFactorDeltaP99 (default 0.05)
//
// Hysteresis: the gate must clear for ConsecutiveTicks (default 3)
// before the observer logs. Any miss — including a transient stats
// fetch failure — resets the counter; the gate must be reproducibly
// clear, not "clear plus we lost sight for a moment".
package autopromote

import (
	"context"
	"sync"
	"time"

	"github.com/helmedeiros/model-registry/internal/envstate"
	"github.com/helmedeiros/model-registry/internal/shadowstats"
)

type Logger interface {
	Info(msg string, attrs map[string]any)
}

type Observer struct {
	envs              []string
	envState          envstate.Reader
	shadow            shadowstats.Reader
	logger            Logger
	interval          time.Duration
	since             time.Duration
	minAgreement      float64
	minSamples        float64
	maxFactorDeltaP99 float64
	consecutiveTicks  int

	mu          sync.Mutex
	clearStreak map[string]int
}

type Config struct {
	Envs              []string
	EnvState          envstate.Reader
	Shadow            shadowstats.Reader
	Logger            Logger
	Interval          time.Duration
	Since             time.Duration
	MinAgreement      float64
	MinSamples        float64
	MaxFactorDeltaP99 float64
	ConsecutiveTicks  int
}

const (
	DefaultMinAgreement      = 0.99
	DefaultMinSamples        = 10000
	DefaultMaxFactorDeltaP99 = 0.05
	DefaultConsecutiveTicks  = 3
	DefaultSince             = 5 * time.Minute
)

func New(c Config) *Observer {
	if c.MinAgreement == 0 {
		c.MinAgreement = DefaultMinAgreement
	}
	if c.MinSamples == 0 {
		c.MinSamples = DefaultMinSamples
	}
	if c.MaxFactorDeltaP99 == 0 {
		c.MaxFactorDeltaP99 = DefaultMaxFactorDeltaP99
	}
	if c.ConsecutiveTicks == 0 {
		c.ConsecutiveTicks = DefaultConsecutiveTicks
	}
	if c.Since == 0 {
		c.Since = DefaultSince
	}
	return &Observer{
		envs:              c.Envs,
		envState:          c.EnvState,
		shadow:            c.Shadow,
		logger:            c.Logger,
		interval:          c.Interval,
		since:             c.Since,
		minAgreement:      c.MinAgreement,
		minSamples:        c.MinSamples,
		maxFactorDeltaP99: c.MaxFactorDeltaP99,
		consecutiveTicks:  c.ConsecutiveTicks,
		clearStreak:       map[string]int{},
	}
}

func (o *Observer) Start(ctx context.Context) {
	ticker := time.NewTicker(o.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.observeOnce(ctx)
		}
	}
}

func (o *Observer) observeOnce(ctx context.Context) {
	// Stats are not env-keyed (ADR-0013); fetch once per tick.
	stats, err := o.shadow.Stats(ctx, o.since)
	if err != nil {
		o.log("registry.autopromote.stats_unavailable", map[string]any{
			"error": err.Error(),
		})
		for _, env := range o.envs {
			o.resetStreak(env)
		}
		return
	}
	for _, env := range o.envs {
		o.observeEnv(ctx, env, stats)
	}
}

func (o *Observer) observeEnv(ctx context.Context, env string, stats shadowstats.Stats) {
	state, err := o.envState.Get(ctx, env)
	if err != nil || state.Challenger == nil {
		o.resetStreak(env)
		return
	}
	if !o.gateCleared(stats) {
		o.resetStreak(env)
		return
	}
	streak := o.bumpStreak(env)
	if streak < o.consecutiveTicks {
		return
	}
	o.log("registry.autopromote.gate_cleared", map[string]any{
		"env":                env,
		"challenger_hash":    string(state.Challenger.Hash),
		"agreement":          stats.AgreementRate,
		"samples":            stats.AgreementSamples,
		"factor_delta_p99":   stats.FactorDeltaP99,
		"consecutive_ticks":  streak,
		"mode":               "log_only",
		"recommended_action": "mrctl promote --role champion --hash " + string(state.Challenger.Hash),
	})
}

func (o *Observer) gateCleared(s shadowstats.Stats) bool {
	return s.AgreementRate >= o.minAgreement &&
		s.AgreementSamples >= o.minSamples &&
		s.FactorDeltaP99 <= o.maxFactorDeltaP99
}

func (o *Observer) bumpStreak(env string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.clearStreak[env]++
	return o.clearStreak[env]
}

func (o *Observer) resetStreak(env string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.clearStreak, env)
}

func (o *Observer) log(msg string, attrs map[string]any) {
	if o.logger == nil {
		return
	}
	o.logger.Info(msg, attrs)
}
