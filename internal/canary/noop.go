package canary

import "context"

type NoopDecider struct{}

func (NoopDecider) Decide(_ context.Context, _ string) (Decision, Observation, error) {
	return DecisionInconclusive, Observation{}, nil
}
