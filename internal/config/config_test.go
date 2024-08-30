package config_test

import (
	"errors"
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/config"
)

func TestDefaultMatchesADR0003(t *testing.T) {
	d := config.Default()
	if d.Addr != ":8090" {
		t.Fatalf("Addr=%q", d.Addr)
	}
	if d.StoreBackend != config.StoreBackendFS {
		t.Fatalf("StoreBackend=%q", d.StoreBackend)
	}
	if d.StoreRoot != "./data" {
		t.Fatalf("StoreRoot=%q", d.StoreRoot)
	}
	if d.OTelExporter != "none" {
		t.Fatalf("OTelExporter=%q", d.OTelExporter)
	}
	if d.LogLevel != "info" {
		t.Fatalf("LogLevel=%q", d.LogLevel)
	}
	if d.ShutdownTimeout != 10*time.Second {
		t.Fatalf("ShutdownTimeout=%s", d.ShutdownTimeout)
	}
}

func TestLoadFromArgsHonoursFlags(t *testing.T) {
	cfg, _, err := config.LoadFromArgs([]string{
		"--addr", ":9090",
		"--store-backend", "mem",
		"--store-root", "/var/registry",
		"--log-level", "debug",
		"--shutdown-timeout", "30s",
	})
	if err != nil {
		t.Fatalf("LoadFromArgs: %v", err)
	}
	if cfg.Addr != ":9090" || cfg.StoreBackend != "mem" || cfg.LogLevel != "debug" || cfg.ShutdownTimeout != 30*time.Second {
		t.Fatalf("unexpected: %+v", cfg)
	}
}

func TestLoadFromArgsHonoursEnvWhenFlagAbsent(t *testing.T) {
	t.Setenv("REGISTRY_ADDR", ":7777")
	t.Setenv("REGISTRY_LOG_LEVEL", "warn")

	cfg, _, err := config.LoadFromArgs(nil)
	if err != nil {
		t.Fatalf("LoadFromArgs: %v", err)
	}
	if cfg.Addr != ":7777" || cfg.LogLevel != "warn" {
		t.Fatalf("env overlay missed: addr=%q level=%q", cfg.Addr, cfg.LogLevel)
	}
}

func TestLoadFromArgsFlagBeatsEnv(t *testing.T) {
	t.Setenv("REGISTRY_ADDR", ":7777")
	cfg, _, err := config.LoadFromArgs([]string{"--addr", ":8888"})
	if err != nil {
		t.Fatalf("LoadFromArgs: %v", err)
	}
	if cfg.Addr != ":8888" {
		t.Fatalf("flag must beat env: %q", cfg.Addr)
	}
}

func TestLoadFromArgsHelpReturnsErrHelp(t *testing.T) {
	_, _, err := config.LoadFromArgs([]string{"--help"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("--help should propagate flag.ErrHelp; got %v", err)
	}
}

func TestValidateRejectsUnknownStoreBackend(t *testing.T) {
	cfg := config.Default()
	cfg.StoreBackend = "redis"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "store-backend") {
		t.Fatalf("expected store-backend error, got %v", err)
	}
}

func TestValidateRejectsUnknownOTelExporter(t *testing.T) {
	cfg := config.Default()
	cfg.OTelExporter = "jaeger-direct"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "otel-exporter") {
		t.Fatalf("expected otel-exporter error, got %v", err)
	}
}

func TestValidateRequiresEndpointWhenOTLP(t *testing.T) {
	cfg := config.Default()
	cfg.OTelExporter = "otlp"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "otel-endpoint") {
		t.Fatalf("expected otel-endpoint error, got %v", err)
	}
}

func TestValidateRejectsNonPositiveShutdownTimeout(t *testing.T) {
	cfg := config.Default()
	cfg.ShutdownTimeout = 0
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "shutdown-timeout") {
		t.Fatalf("expected shutdown-timeout error, got %v", err)
	}
}

func TestLoadFromArgsInvalidShutdownTimeout(t *testing.T) {
	_, _, err := config.LoadFromArgs([]string{"--shutdown-timeout", "tenseconds"})
	if err == nil || !strings.Contains(err.Error(), "shutdown-timeout") {
		t.Fatalf("expected shutdown-timeout parse error, got %v", err)
	}
}

func TestLoadFromArgsWriteRateFlagsHonoured(t *testing.T) {
	cfg, _, err := config.LoadFromArgs([]string{
		"--write-rate-refill", "0s",
		"--write-rate-burst", "0",
	})
	if err != nil {
		t.Fatalf("LoadFromArgs: %v", err)
	}
	if cfg.WriteRateRefill != 0 || cfg.WriteRateBurst != 0 {
		t.Fatalf("WriteRate*: refill=%s burst=%d want both zero", cfg.WriteRateRefill, cfg.WriteRateBurst)
	}
}

func TestLoadFromArgsWriteRateBurstFromEnv(t *testing.T) {
	t.Setenv("REGISTRY_WRITE_RATE_BURST", "5")
	cfg, _, err := config.LoadFromArgs(nil)
	if err != nil {
		t.Fatalf("LoadFromArgs: %v", err)
	}
	if cfg.WriteRateBurst != 5 {
		t.Fatalf("WriteRateBurst=%d want 5 from env", cfg.WriteRateBurst)
	}
}

func TestLoadFromArgsInvalidWriteRateRefill(t *testing.T) {
	_, _, err := config.LoadFromArgs([]string{"--write-rate-refill", "garbage"})
	if err == nil || !strings.Contains(err.Error(), "write-rate-refill") {
		t.Fatalf("expected write-rate-refill error, got %v", err)
	}
}

func TestLoadFromArgsInvalidWriteRateBurst(t *testing.T) {
	_, _, err := config.LoadFromArgs([]string{"--write-rate-burst", "garbage"})
	if err == nil || !strings.Contains(err.Error(), "write-rate-burst") {
		t.Fatalf("expected write-rate-burst error, got %v", err)
	}
}
