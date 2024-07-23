package jsonlog_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/observability/jsonlog"
)

func TestParseLevelKnownValues(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want jsonlog.Level
	}{
		{"debug", jsonlog.LevelDebug},
		{"INFO", jsonlog.LevelInfo},
		{" warn ", jsonlog.LevelWarn},
		{"Error", jsonlog.LevelError},
	} {
		got, err := jsonlog.ParseLevel(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("ParseLevel(%q)=%v err=%v want %v", tc.in, got, err, tc.want)
		}
	}
}

func TestParseLevelRejectsUnknown(t *testing.T) {
	if _, err := jsonlog.ParseLevel("verbose"); err == nil {
		t.Fatal("expected error for unknown level")
	}
}

func TestLevelStringIsRoundTripFaithful(t *testing.T) {
	for _, l := range []jsonlog.Level{jsonlog.LevelDebug, jsonlog.LevelInfo, jsonlog.LevelWarn, jsonlog.LevelError} {
		round, err := jsonlog.ParseLevel(l.String())
		if err != nil || round != l {
			t.Fatalf("round-trip lost level %v: parsed %v err %v", l, round, err)
		}
	}
}

func TestWriteEmitsPlatformShape(t *testing.T) {
	var buf bytes.Buffer
	fixed := time.Date(2024, 7, 22, 14, 0, 0, 0, time.UTC)
	l := jsonlog.New(&buf,
		jsonlog.WithLevel(jsonlog.LevelInfo),
		jsonlog.WithClock(func() time.Time { return fixed }),
	)
	l.Info("registry.boot", map[string]any{"addr": ":8090", "store_backend": "fs"})

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("emitted line is not valid json: %v\n%s", err, buf.String())
	}
	if got["time"] != "2024-07-22T14:00:00Z" {
		t.Fatalf("time=%v want 2024-07-22T14:00:00Z", got["time"])
	}
	if got["level"] != "info" || got["msg"] != "registry.boot" {
		t.Fatalf("level/msg wrong: %+v", got)
	}
	attrs, ok := got["attrs"].(map[string]any)
	if !ok || attrs["addr"] != ":8090" || attrs["store_backend"] != "fs" {
		t.Fatalf("attrs missing or wrong: %+v", got["attrs"])
	}
}

func TestNilAttrsOmittedFromOutput(t *testing.T) {
	var buf bytes.Buffer
	l := jsonlog.New(&buf, jsonlog.WithLevel(jsonlog.LevelInfo))
	l.Info("startup", nil)
	if strings.Contains(buf.String(), "attrs") {
		t.Fatalf("attrs field should be omitted when nil: %s", buf.String())
	}
}

func TestEventsBelowLevelAreDropped(t *testing.T) {
	var buf bytes.Buffer
	l := jsonlog.New(&buf, jsonlog.WithLevel(jsonlog.LevelWarn))
	l.Debug("noisy", nil)
	l.Info("quiet", nil)
	l.Warn("loud", nil)
	l.Error("loudest", nil)
	if !strings.Contains(buf.String(), "\"msg\":\"loud\"") || !strings.Contains(buf.String(), "\"msg\":\"loudest\"") {
		t.Fatalf("warn/error must pass: %s", buf.String())
	}
	if strings.Contains(buf.String(), "\"msg\":\"noisy\"") || strings.Contains(buf.String(), "\"msg\":\"quiet\"") {
		t.Fatalf("below-threshold events must drop: %s", buf.String())
	}
}

func TestEnabledMirrorsThreshold(t *testing.T) {
	l := jsonlog.New(&bytes.Buffer{}, jsonlog.WithLevel(jsonlog.LevelWarn))
	if l.Enabled(jsonlog.LevelInfo) || l.Enabled(jsonlog.LevelDebug) {
		t.Fatal("Enabled must report false for levels below threshold")
	}
	if !l.Enabled(jsonlog.LevelWarn) || !l.Enabled(jsonlog.LevelError) {
		t.Fatal("Enabled must report true for the threshold and above")
	}
}

type failingWriter struct{ err error }

func (f failingWriter) Write(_ []byte) (int, error) { return 0, f.err }

func TestErrorHandlerInvokedOnWriteFailure(t *testing.T) {
	wantErr := &writeErr{msg: "pipe broke"}
	var captured error
	l := jsonlog.New(failingWriter{err: wantErr},
		jsonlog.WithLevel(jsonlog.LevelInfo),
		jsonlog.WithErrorHandler(func(err error) { captured = err }),
	)
	l.Info("any", nil)
	if captured == nil || captured.Error() != "pipe broke" {
		t.Fatalf("error handler not invoked or wrong error: %v", captured)
	}
}

type writeErr struct{ msg string }

func (w *writeErr) Error() string { return w.msg }

func TestConcurrentWritesAreSerialised(t *testing.T) {
	// Without the lock, two goroutines interleaving json.Encoder.Encode
	// writes would produce corrupt JSON. Each line in the output must
	// parse cleanly.
	var buf bytes.Buffer
	l := jsonlog.New(&buf, jsonlog.WithLevel(jsonlog.LevelInfo))

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l.Info("concurrent", map[string]any{"i": i})
		}(i)
	}
	wg.Wait()

	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var got map[string]any
		if err := json.Unmarshal(line, &got); err != nil {
			t.Fatalf("interleaved write produced unparseable line: %v\n%s", err, line)
		}
	}
}

