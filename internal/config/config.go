// Package config holds the typed Config the cmd shell binds via
// --flag (canonical) and REGISTRY_* env (12-factor convenience). All
// validation runs at boot — a typo'd value surfaces with a clear
// error rather than silently defaulting.
package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the operator-facing configuration contract for the model-
// registry binary. Defaults match ADR-0003.
type Config struct {
	Addr               string
	StoreBackend       string
	StoreRoot          string
	OTelExporter       string
	OTelEndpoint       string
	LogLevel           string
	ShutdownTimeout    time.Duration
	InstancesConfig    string
	CanaryPromURL      string
	CanaryWindow       time.Duration
	CanaryThreshold    float64
	CanaryPollEvery    time.Duration
	CanaryMinSamples   int
	WriteRateRefill    time.Duration
	WriteRateBurst     int
	BusinessStatsPromURL string
	ShadowStatsPromURL   string
	ReconcileInterval    time.Duration
	ReconcileLivenessInterval time.Duration
}

const (
	StoreBackendFS  = "fs"
	StoreBackendMem = "mem"
)

// Default returns a Config populated with the ADR-0003 defaults. The
// cmd shell starts from Default and overlays flags + env.
func Default() Config {
	return Config{
		Addr:             ":8090",
		StoreBackend:     StoreBackendFS,
		StoreRoot:        "./data",
		OTelExporter:     "none",
		OTelEndpoint:     "",
		LogLevel:         "info",
		ShutdownTimeout:  10 * time.Second,
		CanaryWindow:     5 * time.Minute,
		CanaryThreshold:  0.01,
		CanaryPollEvery:  30 * time.Second,
		CanaryMinSamples: 100,
		WriteRateRefill:  10 * time.Second,
		WriteRateBurst:   2,
	}
}

// LoadFromArgs parses argv as flags layered on top of process env. The
// returned *flag.FlagSet is exposed so the caller can redirect output
// and print usage on error.
func LoadFromArgs(args []string) (Config, *flag.FlagSet, error) {
	cfg := Default()
	fs := flag.NewFlagSet("model-registry", flag.ContinueOnError)
	fs.StringVar(&cfg.Addr, "addr", envOr("REGISTRY_ADDR", cfg.Addr), "HTTP listen address (e.g. :8090).")
	fs.StringVar(&cfg.StoreBackend, "store-backend", envOr("REGISTRY_STORE_BACKEND", cfg.StoreBackend), "Store backing: fs|mem.")
	fs.StringVar(&cfg.StoreRoot, "store-root", envOr("REGISTRY_STORE_ROOT", cfg.StoreRoot), "Filesystem root for the fs backing.")
	fs.StringVar(&cfg.OTelExporter, "otel-exporter", envOr("REGISTRY_OTEL_EXPORTER", cfg.OTelExporter), "OTel span exporter: none|otlp.")
	fs.StringVar(&cfg.OTelEndpoint, "otel-endpoint", envOr("REGISTRY_OTEL_ENDPOINT", cfg.OTelEndpoint), "OTLP collector address; required when --otel-exporter=otlp.")
	fs.StringVar(&cfg.LogLevel, "log-level", envOr("REGISTRY_LOG_LEVEL", cfg.LogLevel), "Log level: debug|info|warn|error.")
	fs.StringVar(&cfg.InstancesConfig, "instances-config", envOr("REGISTRY_INSTANCES_CONFIG", cfg.InstancesConfig), "Path to JSON config mapping env -> markup-svc base URLs (enables POST /promote).")
	fs.StringVar(&cfg.CanaryPromURL, "canary-prom-url", envOr("REGISTRY_CANARY_PROM_URL", cfg.CanaryPromURL), "Prometheus base URL for the post-promote canary (e.g. http://prometheus:9090). Empty = canary disabled.")
	canaryWindowStr := envOr("REGISTRY_CANARY_WINDOW", cfg.CanaryWindow.String())
	fs.StringVar(&canaryWindowStr, "canary-window", canaryWindowStr, "Canary observation window (Go duration).")
	canaryPollStr := envOr("REGISTRY_CANARY_POLL_EVERY", cfg.CanaryPollEvery.String())
	fs.StringVar(&canaryPollStr, "canary-poll-every", canaryPollStr, "Canary polling interval (Go duration).")
	thresholdStr := envOr("REGISTRY_CANARY_THRESHOLD", "")
	fs.StringVar(&thresholdStr, "canary-threshold", thresholdStr, "Canary error-rate threshold; rollback when observed rate exceeds this (e.g. 0.01).")
	fs.IntVar(&cfg.CanaryMinSamples, "canary-min-samples", cfg.CanaryMinSamples, "Minimum markup_decide_total sample count in the window required to reach a non-inconclusive decision.")
	writeRefillStr := envOr("REGISTRY_WRITE_RATE_REFILL", cfg.WriteRateRefill.String())
	fs.StringVar(&writeRefillStr, "write-rate-refill", writeRefillStr, "Token-bucket refill interval per env on /promote + /rollback (Go duration). Empty/0 disables the limiter.")
	writeBurstStr := envOr("REGISTRY_WRITE_RATE_BURST", strconv.Itoa(cfg.WriteRateBurst))
	fs.StringVar(&writeBurstStr, "write-rate-burst", writeBurstStr, "Token-bucket burst per env on /promote + /rollback.")
	fs.StringVar(&cfg.BusinessStatsPromURL, "business-stats-prom-url", envOr("REGISTRY_BUSINESS_STATS_PROM_URL", cfg.BusinessStatsPromURL), "Prometheus base URL for /env/<env>/business-stats. Empty = endpoint disabled.")
	fs.StringVar(&cfg.ShadowStatsPromURL, "shadow-stats-prom-url", envOr("REGISTRY_SHADOW_STATS_PROM_URL", cfg.ShadowStatsPromURL), "Prometheus base URL for /shadow-stats (markup-svc challenger comparison metrics, ADR-0013). Empty = endpoint disabled.")
	reconcileStr := envOr("REGISTRY_RECONCILE_INTERVAL", "")
	fs.StringVar(&reconcileStr, "reconcile-interval", reconcileStr, "Background reconciliation period for re-pushing the current Challenger envstate to markup-svc (Go duration). Empty = disabled. Recommended 5m-30m in production so a markup-svc restart recovers without operator intervention within one tick.")
	reconcileLivenessStr := envOr("REGISTRY_RECONCILE_LIVENESS_INTERVAL", "")
	fs.StringVar(&reconcileLivenessStr, "reconcile-liveness-interval", reconcileLivenessStr, "Per-instance /readyz poll period (Go duration). When a markup-svc instance transitions from not-ready to ready, the reconciler re-pushes the Challenger to that instance immediately. Empty = liveness tracking disabled; recovery waits up to one --reconcile-interval. Recommended 5s-30s in production.")
	// flag.DurationVar cannot be pre-seeded from env; bind a string
	// intermediary so REGISTRY_SHUTDOWN_TIMEOUT resolves before fs.Parse.
	timeoutStr := envOr("REGISTRY_SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout.String())
	fs.StringVar(&timeoutStr, "shutdown-timeout", timeoutStr, "Graceful drain budget (Go duration).")

	if err := fs.Parse(args); err != nil {
		return Config{}, fs, err
	}

	parsedTimeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		return Config{}, fs, fmt.Errorf("config: shutdown-timeout %q: %w", timeoutStr, err)
	}
	cfg.ShutdownTimeout = parsedTimeout

	parsedWindow, err := time.ParseDuration(canaryWindowStr)
	if err != nil {
		return Config{}, fs, fmt.Errorf("config: canary-window %q: %w", canaryWindowStr, err)
	}
	cfg.CanaryWindow = parsedWindow

	parsedPoll, err := time.ParseDuration(canaryPollStr)
	if err != nil {
		return Config{}, fs, fmt.Errorf("config: canary-poll-every %q: %w", canaryPollStr, err)
	}
	cfg.CanaryPollEvery = parsedPoll

	if reconcileStr != "" {
		parsedReconcile, err := time.ParseDuration(reconcileStr)
		if err != nil {
			return Config{}, fs, fmt.Errorf("config: reconcile-interval %q: %w", reconcileStr, err)
		}
		cfg.ReconcileInterval = parsedReconcile
	}
	if reconcileLivenessStr != "" {
		parsedLiveness, err := time.ParseDuration(reconcileLivenessStr)
		if err != nil {
			return Config{}, fs, fmt.Errorf("config: reconcile-liveness-interval %q: %w", reconcileLivenessStr, err)
		}
		cfg.ReconcileLivenessInterval = parsedLiveness
	}

	if thresholdStr != "" {
		f, perr := strconv.ParseFloat(thresholdStr, 64)
		if perr != nil {
			return Config{}, fs, fmt.Errorf("config: canary-threshold %q: %w", thresholdStr, perr)
		}
		cfg.CanaryThreshold = f
	}

	parsedRefill, err := time.ParseDuration(writeRefillStr)
	if err != nil {
		return Config{}, fs, fmt.Errorf("config: write-rate-refill %q: %w", writeRefillStr, err)
	}
	cfg.WriteRateRefill = parsedRefill

	parsedBurst, err := strconv.Atoi(writeBurstStr)
	if err != nil {
		return Config{}, fs, fmt.Errorf("config: write-rate-burst %q: %w", writeBurstStr, err)
	}
	cfg.WriteRateBurst = parsedBurst

	if err := cfg.Validate(); err != nil {
		return Config{}, fs, err
	}
	return cfg, fs, nil
}

// Validate enforces cross-field invariants the flag library cannot.
// Returns nil for a wirable Config.
func (c Config) Validate() error {
	switch c.StoreBackend {
	case StoreBackendFS, StoreBackendMem:
	default:
		return fmt.Errorf("config: store-backend %q (want fs|mem)", c.StoreBackend)
	}
	switch strings.ToLower(c.OTelExporter) {
	case "none", "otlp":
	default:
		return fmt.Errorf("config: otel-exporter %q (want none|otlp)", c.OTelExporter)
	}
	if strings.EqualFold(c.OTelExporter, "otlp") && strings.TrimSpace(c.OTelEndpoint) == "" {
		return fmt.Errorf("config: otel-endpoint required when otel-exporter=otlp")
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("config: shutdown-timeout must be positive, got %s", c.ShutdownTimeout)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
