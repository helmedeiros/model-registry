// Package reconciler periodically re-pushes the current envstate
// Challenger hash to markup-svc so a markup-svc restart does not
// leave the data plane silently stale relative to the registry's
// intent. The push is byte-idempotent on markup-svc's
// /admin/load-challenger; re-pushing identical bytes is observably
// equivalent to a no-op except for the response shape, so the
// reconciliation cost scales with envs × instances × interval, not
// with /decide QPS.
//
// In addition to the periodic full-reconciliation tick, the
// Reconciler optionally polls each instance's /readyz at a tighter
// cadence. When an instance transitions from not-ready to ready
// (markup-svc just restarted and came back up), the Reconciler
// re-pushes the Challenger to that single instance immediately
// rather than waiting up to one full reconciliation interval.
package reconciler

import (
	"context"
	"net/http"
	"sync"
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
	envs             []string
	envState         envstate.Reader
	artifacts        store.Reader
	discovery        instances.Discovery
	deployer         deployer.Deployer
	logger           Logger
	interval         time.Duration
	livenessInterval time.Duration
	livenessClient   *http.Client
	livenessTimeout  time.Duration

	mu         sync.Mutex
	readyState map[string]bool
}

type Option func(*Reconciler)

// WithLivenessInterval enables per-instance /readyz polling at the
// supplied cadence. On a not-ready → ready transition the Reconciler
// re-pushes the current Challenger to that single instance. 0 (the
// default) disables liveness tracking; recovery then waits for the
// next full-reconciliation tick.
func WithLivenessInterval(d time.Duration) Option {
	return func(r *Reconciler) { r.livenessInterval = d }
}

// WithLivenessHTTPClient lets tests inject httptest.Server's client.
// Production uses an http.Client with a short timeout.
func WithLivenessHTTPClient(c *http.Client) Option {
	return func(r *Reconciler) { r.livenessClient = c }
}

// WithLivenessTimeout caps each /readyz probe's wall-clock.
func WithLivenessTimeout(d time.Duration) Option {
	return func(r *Reconciler) { r.livenessTimeout = d }
}

func New(envs []string, envState envstate.Reader, artifacts store.Reader, discovery instances.Discovery, dep deployer.Deployer, logger Logger, interval time.Duration, opts ...Option) *Reconciler {
	r := &Reconciler{
		envs:            envs,
		envState:        envState,
		artifacts:       artifacts,
		discovery:       discovery,
		deployer:        dep,
		logger:          logger,
		interval:        interval,
		livenessClient:  &http.Client{Timeout: 2 * time.Second},
		livenessTimeout: 1 * time.Second,
		readyState:      map[string]bool{},
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Start blocks until ctx ends. Runs the periodic full-reconciliation
// loop. When configured with WithLivenessInterval, also runs a
// separate goroutine that polls /readyz and fires per-instance
// recovery on not-ready → ready transitions.
func (r *Reconciler) Start(ctx context.Context) {
	if r.livenessInterval > 0 {
		go r.runLiveness(ctx)
	}
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

func (r *Reconciler) runLiveness(ctx context.Context) {
	ticker := time.NewTicker(r.livenessInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.checkLiveness(ctx)
		}
	}
}

func (r *Reconciler) checkLiveness(ctx context.Context) {
	for _, env := range r.envs {
		targets, err := r.discovery.Instances(ctx, env)
		if err != nil {
			continue
		}
		for _, t := range targets {
			ready := r.probeReady(ctx, t.URL)
			r.mu.Lock()
			wasReady, known := r.readyState[t.URL]
			r.readyState[t.URL] = ready
			r.mu.Unlock()
			// First-poll empty→ready also fires; that's deliberate. We
			// don't know if markup-svc had a challenger loaded before
			// the reconciler started, so reconciling on first sight is
			// the safe assumption.
			if ready && (!known || !wasReady) {
				r.recoverInstance(ctx, env, t)
			}
		}
	}
}

func (r *Reconciler) probeReady(ctx context.Context, url string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, r.livenessTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url+"/readyz", nil)
	if err != nil {
		return false
	}
	resp, err := r.livenessClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

func (r *Reconciler) recoverInstance(ctx context.Context, env string, target instances.Instance) {
	state, err := r.envState.Get(ctx, env)
	if err != nil || state.Challenger == nil {
		return
	}
	source, ct, err := r.artifacts.GetMember(ctx, state.Challenger.Hash, store.MemberSource)
	if err != nil {
		r.log("registry.reconciler.recover.source_missing", map[string]any{"env": env, "instance": target.URL, "error": err.Error()})
		return
	}
	result, _ := r.deployer.DeployChallenger(ctx, []instances.Instance{target}, deployer.Body{Bytes: source, ContentType: string(ct)})
	r.log("registry.reconciler.recovered_instance", map[string]any{
		"env":      env,
		"instance": target.URL,
		"hash":     string(state.Challenger.Hash),
		"outcome":  string(result.Outcome),
	})
}

func (r *Reconciler) log(msg string, attrs map[string]any) {
	if r.logger == nil {
		return
	}
	r.logger.Info(msg, attrs)
}
