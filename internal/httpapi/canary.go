package httpapi

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/canary"
	"github.com/helmedeiros/model-registry/internal/deployer"
	"github.com/helmedeiros/model-registry/internal/envstate"
	"github.com/helmedeiros/model-registry/internal/instances"
	"github.com/helmedeiros/model-registry/internal/store"
)

// CanaryObserver is the seam PromoteDeps depends on. The concrete
// supervisor lives below; tests substitute a thin fake.
type CanaryObserver interface {
	Observe(ctx context.Context, env, deployedHash, operator string)
}

// canaryMetricsOrDefault returns the supplied metrics or a no-op so
// the supervisor methods can call unconditionally.
func canaryMetricsOrDefault(m CanaryMetrics) CanaryMetrics {
	if m == nil {
		return noopMetrics{}
	}
	return m
}

type CanarySupervisor struct {
	Decider   canary.Decider
	Artifacts store.Reader
	EnvState  envstate.Store
	Discovery instances.Discovery
	Deployer  deployer.Deployer
	Audit     audit.Writer
	ULID      ULIDSource
	Logger    AccessSink
	Metrics   CanaryMetrics
	Now       func() time.Time
}

func (s *CanarySupervisor)Observe(ctx context.Context, env, deployedHash, operator string) {
	if s.Decider == nil {
		return
	}
	s.Metrics = canaryMetricsOrDefault(s.Metrics)
	if s.Now == nil {
		s.Now = time.Now
	}
	decision, obs, err := s.Decider.Decide(ctx, env)
	if err != nil && !errors.Is(err, context.Canceled) {
		s.Logger.Info("registry.canary.observe_failed", map[string]any{
			"env":             env,
			"deployed_hash":   deployedHash,
			"error":           err.Error(),
			"upstream_failed": errors.Is(err, canary.ErrUpstreamUnreachable),
		})
	}
	s.Metrics.RecordCanary(env, string(decision))
	s.recordCanaryAudit(ctx, env, deployedHash, operator, decision, obs)

	if decision != canary.DecisionRolledBack {
		s.Logger.Info("registry.canary.decision", map[string]any{
			"env":           env,
			"deployed_hash": deployedHash,
			"decision":      string(decision),
			"error_rate":    obs.ErrorRate,
			"samples":       obs.SampleCount,
		})
		return
	}

	if err := s.autoRollback(ctx, env, deployedHash, operator, obs); err != nil {
		s.Logger.Info("registry.canary.rollback_failed", map[string]any{
			"env":           env,
			"deployed_hash": deployedHash,
			"error":         err.Error(),
		})
	}
}

func (s *CanarySupervisor)autoRollback(ctx context.Context, env, deployedHash, operator string, obs canary.Observation) error {
	prev, err := s.EnvState.PreviousChampion(ctx, env)
	if err != nil {
		return err
	}
	bundle, err := s.Artifacts.GetBundle(ctx, prev)
	if err != nil {
		return err
	}
	if bundle.State == store.StateDeprecated {
		return errors.New("previous champion is deprecated")
	}
	sourceBytes, contentType, err := s.Artifacts.GetMember(ctx, prev, store.MemberSource)
	if err != nil {
		return err
	}
	targets, err := s.Discovery.Instances(ctx, env)
	if err != nil {
		return err
	}
	deployResult, err := s.Deployer.Deploy(ctx, targets, deployer.Body{
		ContentType: string(contentType),
		Bytes:       sourceBytes,
	})
	if err != nil {
		return err
	}
	if deployResult.Outcome == deployer.OutcomeFailed {
		return errors.New("canary auto-rollback deploy failed; envstate not committed")
	}
	reason := canaryRollbackReason(obs)
	if _, err := s.EnvState.RollbackChampion(ctx, env, "registry.canary", reason); err != nil {
		return err
	}
	s.Logger.Info("registry.canary.auto_rollback", map[string]any{
		"env":           env,
		"rolled_from":   deployedHash,
		"rolled_to":     string(prev),
		"operator":      operator,
		"error_rate":    obs.ErrorRate,
		"samples":       obs.SampleCount,
	})
	return s.recordAutoRollbackAudit(ctx, env, prev, operator, reason)
}

func canaryRollbackReason(obs canary.Observation) string {
	return "canary auto-rollback: error_rate=" + formatRate(obs.ErrorRate) +
		" > threshold=" + formatRate(obs.Threshold) +
		" over " + obs.Window.String()
}

func formatRate(v float64) string {
	return strconv.FormatFloat(v, 'f', 4, 64)
}

func (s *CanarySupervisor)recordCanaryAudit(ctx context.Context, env, hash, operator string, decision canary.Decision, obs canary.Observation) {
	if s.Audit == nil || s.ULID == nil {
		return
	}
	id, err := s.ULID.New()
	if err != nil {
		return
	}
	_ = s.Audit.Record(ctx, audit.Entry{
		ID:           id,
		Operator:     "registry.canary",
		Action:       "canary_observed",
		Target:       "env/" + env + "/champion",
		ArtifactHash: store.Hash(hash),
		Reason:       string(decision) + " error_rate=" + formatRate(obs.ErrorRate) + " samples=" + strconv.Itoa(obs.SampleCount),
		At:           s.Now(),
		TraceID:      traceIDFromCtx(ctx),
	})
}

func (s *CanarySupervisor)recordAutoRollbackAudit(ctx context.Context, env string, rolledTo store.Hash, operator, reason string) error {
	id, err := s.ULID.New()
	if err != nil {
		return err
	}
	return s.Audit.Record(ctx, audit.Entry{
		ID:           id,
		Operator:     "registry.canary",
		Action:       "auto_rollback",
		Target:       "env/" + env + "/champion",
		ArtifactHash: rolledTo,
		Reason:       reason,
		At:           s.Now(),
		TraceID:      traceIDFromCtx(ctx),
	})
}
