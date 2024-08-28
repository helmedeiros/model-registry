// Package canary observes downstream decide error rate after a /promote
// and decides whether to keep the new champion or roll back. Hexagonal:
// Decider is the port, PromDecider is the Prometheus-backed adapter.
package canary

import (
	"context"
	"errors"
	"time"
)

type Decision string

const (
	DecisionKept         Decision = "kept"
	DecisionRolledBack   Decision = "rolled_back"
	DecisionInconclusive Decision = "inconclusive"
)

type Observation struct {
	ErrorRate   float64
	SampleCount int
	Window      time.Duration
	Threshold   float64
}

type Decider interface {
	Decide(ctx context.Context, env string) (Decision, Observation, error)
}

// ErrUpstreamUnreachable is returned by an adapter when the underlying
// metric store is not reachable for the full window. Distinct from a
// legitimate Inconclusive (not enough samples) so a supervisor can
// branch on whether to retry or surface the gap as an operator concern.
var ErrUpstreamUnreachable = errors.New("canary: upstream metric store unreachable")
