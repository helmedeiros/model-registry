// Package businessstats defines the Reader port for aggregated
// business-outcome metrics. PromReader is the production adapter.
package businessstats

import (
	"context"
	"errors"
	"time"
)

type Stats struct {
	Env          string
	Since        time.Duration
	DecideRPS    OutcomeRPS
	FactorP50    float64
	FactorP95    float64
	FactorP99    float64
	TopRules     []RuleHit
}

type OutcomeRPS struct {
	OK       float64
	Error    float64
	NoMatch  float64
	Total    float64
}

type RuleHit struct {
	Rule          string
	RatePerSecond float64
}

type Reader interface {
	Stats(ctx context.Context, env string, since time.Duration) (Stats, error)
}

type NoopReader struct{}

func (NoopReader) Stats(_ context.Context, _ string, _ time.Duration) (Stats, error) {
	return Stats{}, ErrDisabled
}

var ErrDisabled = errors.New("businessstats: reader not configured")
