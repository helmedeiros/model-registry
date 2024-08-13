package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRun_ServesHealthzReadyzMetricsAndShutsDownCleanly(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	args := []string{
		"--store-backend", "mem",
		"--otel-exporter", "none",
		"--log-level", "warn", // quiet the access-log noise in test output
	}

	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, args, io.Discard, io.Discard, ln)
	}()

	waitListening(t, addr)

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/healthz", http.StatusOK},
		{"/readyz", http.StatusOK},
		{"/metrics", http.StatusOK},
		{"/artifacts", http.StatusOK},
		{"/artifact/unknown", http.StatusNotFound},
		{"/env/production/state", http.StatusOK},
		{"/env/production/history", http.StatusOK},
		{"/audit", http.StatusOK},
		// POST /upload via GET → 405 confirms the route mounted; the
		// happy path is exercised by upload_test.go.
		{"/upload", http.StatusMethodNotAllowed},
	} {
		resp := mustGET(t, "http://"+addr+tc.path)
		if resp.StatusCode != tc.want {
			t.Fatalf("%s: status=%d want %d", tc.path, resp.StatusCode, tc.want)
		}
		_ = resp.Body.Close()
	}

	// Correlation ID minted on the response — confirms the chain runs.
	resp := mustGET(t, "http://"+addr+"/healthz")
	if resp.Header.Get("X-Correlation-ID") == "" {
		t.Fatal("X-Correlation-ID missing — middleware chain not active")
	}
	_ = resp.Body.Close()

	// Now hit /healthz twice so the prom counter has observable values
	// in the exposition.
	for i := 0; i < 2; i++ {
		_ = mustGET(t, "http://"+addr+"/healthz").Body.Close()
	}
	body := mustGETBody(t, "http://"+addr+"/metrics")
	for _, mustContain := range []string{
		"registry_http_requests_total",
		`path="/healthz"`,
	} {
		if !strings.Contains(body, mustContain) {
			t.Fatalf("metrics exposition missing %q\n%s", mustContain, body)
		}
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("Run exit code=%d want 0 on clean shutdown", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within shutdown budget")
	}
}

func TestRun_RejectsInvalidConfig(t *testing.T) {
	args := []string{"--store-backend", "redis"}
	code := Run(context.Background(), args, io.Discard, io.Discard, nil)
	if code == 0 {
		t.Fatal("Run should reject unknown store-backend with non-zero exit")
	}
}

func TestRun_HelpExitsZero(t *testing.T) {
	if code := Run(context.Background(), []string{"--help"}, io.Discard, io.Discard, nil); code != 0 {
		t.Fatalf("--help should exit 0, got %d", code)
	}
}

func TestRun_BootEmitsRegistryBootEvent(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var logs syncBuffer
	go func() {
		_ = Run(ctx, []string{"--store-backend", "mem", "--log-level", "info"}, &logs, io.Discard, ln)
	}()
	waitListening(t, ln.Addr().String())
	cancel()

	// Give Run a moment to write the boot event.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logs.String(), `"msg":"registry.boot"`) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("registry.boot not seen in logs:\n%s", logs.String())
}

func TestRun_FSBackendOpensThreeSQLiteFiles(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	root := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, []string{
			"--store-backend", "fs",
			"--store-root", root,
			"--otel-exporter", "none",
			"--log-level", "warn",
		}, io.Discard, io.Discard, ln)
	}()
	// The listener is pre-created by the test, so waitListening only
	// proves the kernel queues connections — it does NOT prove Run has
	// finished opening the three backings. A short stat poll waits for
	// the files to appear; the assertion is the wiring, not the
	// timing.
	for _, name := range []string{"metadata.db", "envstate.db", "audit.db"} {
		if !waitForFile(t, filepath.Join(root, name), 2*time.Second) {
			t.Fatalf("expected %s under store-root, not found within budget", name)
		}
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("Run exit=%d want 0 on clean shutdown", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not shut down within budget")
	}
}

func waitForFile(t *testing.T, path string, budget time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// --- helpers ---

func waitListening(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server never started listening on %s", addr)
}

func mustGET(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

func mustGETBody(t *testing.T, url string) string {
	t.Helper()
	resp := mustGET(t, url)
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// syncBuffer is a minimal goroutine-safe buffer for capturing the
// jsonlog stream while Run is still writing to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

