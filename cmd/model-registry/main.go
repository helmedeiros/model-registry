// Package main is the model-registry service entry point. main() does
// only composition: parse config, build observability, open the
// substrate, build the router, start the server, wait for a signal,
// shut down cleanly. Business logic lives in internal/.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/audit/fsaudit"
	"github.com/helmedeiros/model-registry/internal/audit/memaudit"
	"github.com/helmedeiros/model-registry/internal/businessstats"
	"github.com/helmedeiros/model-registry/internal/instances"
	"github.com/helmedeiros/model-registry/internal/reconciler"
	"github.com/helmedeiros/model-registry/internal/shadowstats"
	"github.com/helmedeiros/model-registry/internal/canary"
	"github.com/helmedeiros/model-registry/internal/config"
	"github.com/helmedeiros/model-registry/internal/deployer/rolling"
	"github.com/helmedeiros/model-registry/internal/envstate"
	"github.com/helmedeiros/model-registry/internal/envstate/fsstate"
	"github.com/helmedeiros/model-registry/internal/envstate/memstate"
	"github.com/helmedeiros/model-registry/internal/httpapi"
	"github.com/helmedeiros/model-registry/internal/instances/static"
	"github.com/helmedeiros/model-registry/internal/observability/jsonlog"
	"github.com/helmedeiros/model-registry/internal/observability/metrics/prom"
	"github.com/helmedeiros/model-registry/internal/ratelimit"
	regotel "github.com/helmedeiros/model-registry/internal/observability/otel"
	"github.com/helmedeiros/model-registry/internal/store"
	"github.com/helmedeiros/model-registry/internal/store/fsstore"
	"github.com/helmedeiros/model-registry/internal/store/memstore"
	"github.com/helmedeiros/model-registry/internal/ulid"
)

const instrumentationName = "github.com/helmedeiros/model-registry"

func main() {
	code := Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, nil)
	os.Exit(code)
}

// Run is the testable boot entrypoint. Returns the process exit code.
// stdout receives the jsonlog stream; stderr receives flag/config
// errors. Run blocks until parent is cancelled or SIGINT/SIGTERM
// lands. When listener is non-nil it is served directly and cfg.Addr
// is ignored — the seam tests use to bind :0 and observe the port.
func Run(parent context.Context, args []string, stdout, stderr io.Writer, listener net.Listener) int {
	cfg, fs, err := config.LoadFromArgs(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, err)
		fs.SetOutput(stderr)
		fs.Usage()
		return 2
	}

	level, err := jsonlog.ParseLevel(cfg.LogLevel)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	logger := jsonlog.New(stdout, jsonlog.WithLevel(level))

	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	tracer, shutdownTracer, err := regotel.Bootstrap(ctx, regotel.Config{
		Exporter:            cfg.OTelExporter,
		Endpoint:            cfg.OTelEndpoint,
		InstrumentationName: instrumentationName,
	})
	if err != nil {
		bootFailed(logger, "otel", err)
		return 1
	}

	metrics := prom.New()

	// Open order is reversed from ADR-0005's shutdown order so defer's
	// LIFO sequence drains as the ADR specifies: store first → envstate
	// → audit last. Partial-failure bail-outs only close what already
	// opened, so reversing the source order is safe.
	auditLog, closeAudit, err := openAudit(cfg)
	if err != nil {
		bootFailed(logger, "audit", err)
		return 1
	}
	defer func() { _ = closeAudit() }()

	envState, closeEnvState, err := openEnvState(cfg)
	if err != nil {
		bootFailed(logger, "envstate", err)
		return 1
	}
	defer func() { _ = closeEnvState() }()

	st, closeStore, err := openStore(cfg)
	if err != nil {
		bootFailed(logger, "store", err)
		return 1
	}
	defer func() { _ = closeStore() }()

	idgen := ulid.New()
	uploadDeps := httpapi.UploadDeps{
		Substrate: st,
		Audit:     auditLog,
		ULID:      idgen,
		Logger:    logger,
		Metrics:   metrics,
	}
	deps := httpapi.Deps{
		AccessLog: logger,
		Metrics:   metrics,
		PanicSink: logger,
		Tracer:    tracer,
		Ready:     readyFor(st),
		Artifacts: st,
		EnvState:  envState,
		Audit:     auditLog,
		Upload:    &uploadDeps,
	}
	limiter := buildWriteLimiter(cfg)
	if promoteDeps, err := buildPromoteDeps(cfg, st, envState, auditLog, idgen, logger); err == nil {
		promoteDeps.Metrics = metrics
		promoteDeps.Limiter = limiter
		if sup := buildCanarySupervisor(cfg, promoteDeps, metrics); sup != nil {
			promoteDeps.Canary = sup
			logger.Info("registry.canary.enabled", map[string]any{
				"prom_url":  cfg.CanaryPromURL,
				"window":    cfg.CanaryWindow.String(),
				"threshold": cfg.CanaryThreshold,
			})
		}
		deps.Promote = promoteDeps
		deps.Rollback = &httpapi.RollbackDeps{
			Artifacts: promoteDeps.Artifacts,
			EnvState:  promoteDeps.EnvState,
			Audit:     promoteDeps.Audit,
			Discovery: promoteDeps.Discovery,
			Deployer:  promoteDeps.Deployer,
			ULID:      promoteDeps.ULID,
			Logger:    promoteDeps.Logger,
			Metrics:   metrics,
			Limiter:   limiter,
		}
		deps.Reject = &httpapi.RejectDeps{
			EnvState:  promoteDeps.EnvState,
			Audit:     promoteDeps.Audit,
			ULID:      promoteDeps.ULID,
			Logger:    promoteDeps.Logger,
			Metrics:   metrics,
			Discovery: promoteDeps.Discovery,
			Deployer:  promoteDeps.Deployer,
		}
	} else {
		logger.Info("registry.promote.disabled", map[string]any{"reason": err.Error()})
	}
	if reader := buildBusinessStatsReader(cfg); reader != nil {
		deps.BusinessStats = &httpapi.BusinessStatsDeps{Reader: reader}
		logger.Info("registry.business_stats.enabled", map[string]any{"prom_url": cfg.BusinessStatsPromURL})
	}
	if reader := buildShadowStatsReader(cfg); reader != nil {
		deps.ShadowStats = &httpapi.ShadowStatsDeps{Reader: reader}
		logger.Info("registry.shadow_stats.enabled", map[string]any{"prom_url": cfg.ShadowStatsPromURL})
	}
	if cfg.ReconcileInterval > 0 && deps.Promote != nil {
		if lister, ok := deps.Promote.Discovery.(instances.EnvLister); ok {
			rec := reconciler.New(lister.Envs(), deps.EnvState, deps.Artifacts, deps.Promote.Discovery, deps.Promote.Deployer, logger, cfg.ReconcileInterval)
			go rec.Start(ctx)
			logger.Info("registry.reconciler.enabled", map[string]any{
				"interval":  cfg.ReconcileInterval.String(),
				"env_count": len(lister.Envs()),
			})
		} else {
			logger.Info("registry.reconciler.disabled", map[string]any{
				"reason": "discovery does not implement EnvLister",
			})
		}
	}
	server := &http.Server{
		Handler: httpapi.NewRouter(deps, metrics.Handler()),
	}

	if listener == nil {
		listener, err = net.Listen("tcp", cfg.Addr)
		if err != nil {
			bootFailed(logger, "listen", err)
			return 1
		}
	}

	logger.Info("registry.boot", map[string]any{
		"addr":          listener.Addr().String(),
		"store_backend": cfg.StoreBackend,
		"store_root":    cfg.StoreRoot,
		"otel_exporter": cfg.OTelExporter,
		"log_level":     cfg.LogLevel,
	})

	serveErr := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
		// Signal received: drain.
	case err := <-serveErr:
		if err != nil {
			logger.Error("registry.serve.failed", map[string]any{"error": err.Error()})
			_ = shutdownTracer(context.Background())
			return 1
		}
		return 0
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	_ = shutdownTracer(shutdownCtx)
	logger.Info("registry.shutdown", map[string]any{
		"shutdown_err": errString(shutdownErr),
	})
	if shutdownErr != nil {
		return 1
	}
	return 0
}

func buildPromoteDeps(cfg config.Config, st store.Store, envState envstate.Store, auditLog audit.Writer, idgen httpapi.ULIDSource, logger httpapi.AccessSink) (*httpapi.PromoteDeps, error) {
	if cfg.InstancesConfig == "" {
		return nil, errors.New("instances-config not set")
	}
	discovery, err := static.Load(cfg.InstancesConfig)
	if err != nil {
		return nil, err
	}
	return &httpapi.PromoteDeps{
		Artifacts: st,
		EnvState:  envState,
		Audit:     auditLog,
		Discovery: discovery,
		Deployer:  rolling.New(),
		ULID:      idgen,
		Logger:    logger,
	}, nil
}

func buildWriteLimiter(cfg config.Config) ratelimit.Limiter {
	if cfg.WriteRateRefill <= 0 || cfg.WriteRateBurst <= 0 {
		return ratelimit.NoopLimiter{}
	}
	return ratelimit.NewTokenBucket(cfg.WriteRateRefill, cfg.WriteRateBurst)
}

func buildBusinessStatsReader(cfg config.Config) businessstats.Reader {
	if cfg.BusinessStatsPromURL == "" {
		return nil
	}
	return businessstats.NewPromReader(cfg.BusinessStatsPromURL)
}

func buildShadowStatsReader(cfg config.Config) shadowstats.Reader {
	if cfg.ShadowStatsPromURL == "" {
		return nil
	}
	return shadowstats.NewPromReader(cfg.ShadowStatsPromURL)
}

func buildCanarySupervisor(cfg config.Config, p *httpapi.PromoteDeps, metrics *prom.HTTPMetrics) *httpapi.CanarySupervisor {
	if cfg.CanaryPromURL == "" {
		return nil
	}
	decider := canary.NewPromDecider(cfg.CanaryPromURL,
		canary.WithPromWindow(cfg.CanaryWindow),
		canary.WithPromPollEvery(cfg.CanaryPollEvery),
		canary.WithPromThreshold(cfg.CanaryThreshold),
		canary.WithPromMinSamples(cfg.CanaryMinSamples),
	)
	return &httpapi.CanarySupervisor{
		Decider:   decider,
		Artifacts: p.Artifacts,
		EnvState:  p.EnvState,
		Discovery: p.Discovery,
		Deployer:  p.Deployer,
		Audit:     p.Audit,
		ULID:      p.ULID,
		Logger:    p.Logger,
		Metrics:   metrics,
	}
}

func openStore(cfg config.Config) (store.Store, func() error, error) {
	switch cfg.StoreBackend {
	case config.StoreBackendMem:
		return memstore.New(), func() error { return nil }, nil
	case config.StoreBackendFS:
		s, err := fsstore.New(cfg.StoreRoot)
		if err != nil {
			return nil, nil, err
		}
		return s, s.Close, nil
	default:
		return nil, nil, fmt.Errorf("unknown store backend %q", cfg.StoreBackend)
	}
}

// openEnvState opens the env-state backing matching cfg.StoreBackend.
// The fs path keeps the file colocated with the artifact substrate.
func openEnvState(cfg config.Config) (envstate.Store, func() error, error) {
	switch cfg.StoreBackend {
	case config.StoreBackendMem:
		return memstate.New(), func() error { return nil }, nil
	case config.StoreBackendFS:
		s, err := fsstate.New(filepath.Join(cfg.StoreRoot, "envstate.db"))
		if err != nil {
			return nil, nil, err
		}
		return s, s.Close, nil
	default:
		return nil, nil, fmt.Errorf("unknown store backend %q", cfg.StoreBackend)
	}
}

func openAudit(cfg config.Config) (audit.Store, func() error, error) {
	switch cfg.StoreBackend {
	case config.StoreBackendMem:
		return memaudit.New(), func() error { return nil }, nil
	case config.StoreBackendFS:
		s, err := fsaudit.New(filepath.Join(cfg.StoreRoot, "audit.db"))
		if err != nil {
			return nil, nil, err
		}
		return s, s.Close, nil
	default:
		return nil, nil, fmt.Errorf("unknown store backend %q", cfg.StoreBackend)
	}
}

// readyFor issues a trivial List against the store on every probe; a
// plain always-true closure would mask a broken handle that opened
// successfully but has since failed.
func readyFor(s store.Store) httpapi.Ready {
	return func() (string, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if _, err := s.List(ctx, store.ListOptions{Limit: 1}); err != nil {
			return err.Error(), false
		}
		return "", true
	}
}

func bootFailed(logger *jsonlog.Logger, component string, err error) {
	logger.Error("registry.boot.failed", map[string]any{
		"component": component,
		"error":     err.Error(),
	})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
