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
	"syscall"
	"time"

	"github.com/helmedeiros/model-registry/internal/audit/memaudit"
	"github.com/helmedeiros/model-registry/internal/config"
	"github.com/helmedeiros/model-registry/internal/envstate/memstate"
	"github.com/helmedeiros/model-registry/internal/httpapi"
	"github.com/helmedeiros/model-registry/internal/observability/jsonlog"
	"github.com/helmedeiros/model-registry/internal/observability/metrics/prom"
	regotel "github.com/helmedeiros/model-registry/internal/observability/otel"
	"github.com/helmedeiros/model-registry/internal/store"
	"github.com/helmedeiros/model-registry/internal/store/fsstore"
	"github.com/helmedeiros/model-registry/internal/store/memstore"
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

	st, closeStore, err := openStore(cfg)
	if err != nil {
		bootFailed(logger, "store", err)
		return 1
	}
	defer func() { _ = closeStore() }()

	envState := memstate.New()
	auditLog := memaudit.New()
	deps := httpapi.Deps{
		AccessLog: logger,
		Metrics:   metrics,
		PanicSink: logger,
		Tracer:    tracer,
		Ready:     readyFor(st),
		Artifacts: st,
		EnvState:  envState,
		Audit:     auditLog,
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
