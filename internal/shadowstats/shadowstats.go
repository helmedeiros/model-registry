// Package shadowstats defines the Reader port for shadow-Decider
// comparison metrics emitted by markup-svc (ADR-0032). PromReader is
// the production adapter; NoopReader handles the disabled-by-config
// case.
//
// The metrics are markup-svc-wide today (no env label). A future
// markup-svc change to add an env label would let this package
// filter per-env; until then Stats reflects whichever markup-svc
// process is being scraped.
package shadowstats

import (
	"context"
	"errors"
	"time"
)

type Stats struct {
	Since               time.Duration
	AgreementRate       float64
	AgreementSamples    float64
	OneSidedChampionRPS float64
	OneSidedChallengerRPS float64
	TimeoutRPS          float64
	ErrorRPS            float64
	FactorDeltaP50      float64
	FactorDeltaP95      float64
	FactorDeltaP99      float64
}

type Reader interface {
	Stats(ctx context.Context, since time.Duration) (Stats, error)
}

type NoopReader struct{}

func (NoopReader) Stats(_ context.Context, _ time.Duration) (Stats, error) {
	return Stats{}, ErrDisabled
}

var ErrDisabled = errors.New("shadowstats: reader not configured")
