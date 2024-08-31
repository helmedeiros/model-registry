// Package reconciler periodically re-pushes the current envstate
// Challenger hash to markup-svc so a markup-svc restart does not
// leave the data plane silently stale relative to the registry's
// intent. The push is byte-idempotent on markup-svc's
// /admin/load-challenger; re-pushing identical bytes is observably
// equivalent to a no-op except for the response shape, so the
// reconciliation cost scales with envs × instances × interval, not
// with /decide QPS.
package reconciler

import (
	"context"
	"time"

	"github.com/helmedeiros/model-registry/internal/deployer"
	"github.com/helmedeiros/model-registry/internal/envstate"
	"github.com/helmedeiros/model-registry/internal/instances"
	"github.com/helmedeiros/model-registry/internal/store"
)

type Logger interface {
	Info(msg string, attrs map[string]any)
}

type Reconciler struct {
	envs      []string
	envState  envstate.Reader
	artifacts store.Reader
	discovery instances.Discovery
	deployer  deployer.Deployer
	logger    Logger
	interval  time.Duration
}

func New(envs []string, envState envstate.Reader, artifacts store.Reader, discovery instances.Discovery, dep deployer.Deployer, logger Logger, interval time.Duration) *Reconciler {
	return &Reconciler{
		envs:      envs,
		envState:  envState,
		artifacts: artifacts,
		discovery: discovery,
		deployer:  dep,
		logger:    logger,
		interval:  interval,
	}
}

// Start blocks until ctx ends. Each tick walks every env, fetches
// the current Challenger from envstate, and re-pushes the source bytes
// to every instance. Pushes failing because of Diagnose rejection are
// logged but not actioned — the operator already saw the rejection on
// the original /promote.
func (r *Reconciler) Start(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.reconcileOnce(ctx)
		}
	}
}

func (r *Reconciler) reconcileOnce(ctx context.Context) {
	for _, env := range r.envs {
		r.reconcileEnv(ctx, env)
	}
}

func (r *Reconciler) reconcileEnv(ctx context.Context, env string) {
	state, err := r.envState.Get(ctx, env)
	if err != nil || state.Challenger == nil {
		return
	}
	hash := state.Challenger.Hash
	source, ct, err := r.artifacts.GetMember(ctx, hash, store.MemberSource)
	if err != nil {
		r.log("registry.reconciler.source_missing", map[string]any{"env": env, "hash": string(hash), "error": err.Error()})
		return
	}
	targets, err := r.discovery.Instances(ctx, env)
	if err != nil {
		r.log("registry.reconciler.discovery_failed", map[string]any{"env": env, "error": err.Error()})
		return
	}
	result, _ := r.deployer.DeployChallenger(ctx, targets, deployer.Body{Bytes: source, ContentType: string(ct)})
	r.log("registry.reconciler.reconciled", map[string]any{
		"env":     env,
		"hash":    string(hash),
		"outcome": string(result.Outcome),
	})
}

func (r *Reconciler) log(msg string, attrs map[string]any) {
	if r.logger == nil {
		return
	}
	r.logger.Info(msg, attrs)
}
