package httpapi

import (
	"context"
	"time"
)

// UploadMetrics is the observability surface POST /upload uses.
type UploadMetrics interface {
	RecordUpload(outcome string)
}

// PromoteMetrics is the observability surface POST /promote uses.
// The deploy counters live here (not on a shared deployer port)
// because the operator-visible outcome is the promotion outcome —
// the per-instance breakdown is a Grafana drill-down.
type PromoteMetrics interface {
	RecordPromotion(env, role, outcome string)
	RecordDeploy(outcome string)
	ObserveDeployDuration(ctx context.Context, d time.Duration)
}

// RollbackMetrics is the observability surface POST /rollback uses.
// Reuses the deploy counters via the shared methods so a Grafana
// panel can sum across promote + rollback for a true "deploys per
// instance" rate.
type RollbackMetrics interface {
	RecordRollback(env, outcome string)
	RecordDeploy(outcome string)
	ObserveDeployDuration(ctx context.Context, d time.Duration)
	RecordStateDrift(env string)
}

// CanaryMetrics is the observability surface the post-promote canary
// supervisor (ADR-0007) uses.
type CanaryMetrics interface {
	RecordCanary(env, decision string)
}

// noopMetrics drops every call. One struct that satisfies all three
// per-handler interfaces is enough — there is exactly one no-op
// behaviour to keep in sync.
type noopMetrics struct{}

func (noopMetrics) RecordUpload(string)                    {}
func (noopMetrics) RecordPromotion(string, string, string) {}
func (noopMetrics) RecordRollback(string, string)          {}
func (noopMetrics) RecordDeploy(string)                    {}
func (noopMetrics) ObserveDeployDuration(context.Context, time.Duration) {}
func (noopMetrics) RecordStateDrift(string)                {}
func (noopMetrics) RecordCanary(string, string)            {}
