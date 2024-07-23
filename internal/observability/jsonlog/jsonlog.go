// Package jsonlog emits the platform-standard JSON log shape
// {time, level, msg, attrs}. The logger is safe for concurrent use;
// events below the configured level allocate nothing.
package jsonlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Level is the emission threshold. Events at a level below the
// configured Level are dropped before any allocation.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// ParseLevel maps the operator-facing strings used by --log-level into
// the typed Level. Unknown strings return an error so a typoed config
// surfaces at boot rather than silently defaulting.
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug, nil
	case "info":
		return LevelInfo, nil
	case "warn":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	default:
		return 0, errors.New("jsonlog: unknown level (want debug|info|warn|error)")
	}
}

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "unknown"
	}
}

// Logger writes structured events to an io.Writer. New events at a
// level below the configured threshold are no-ops.
type Logger struct {
	out   io.Writer
	level Level
	mu    sync.Mutex
	now   func() time.Time
	onErr func(error)
}

// Option configures a Logger at construction.
type Option func(*Logger)

// WithLevel sets the emission threshold. Default is LevelInfo.
func WithLevel(level Level) Option {
	return func(l *Logger) { l.level = level }
}

// WithClock injects a clock for deterministic timestamps in tests.
// Default is time.Now.
func WithClock(now func() time.Time) Option {
	return func(l *Logger) { l.now = now }
}

// WithErrorHandler routes write failures (e.g., broken pipe on stdout)
// to the supplied function so an operator can see the access-log
// failing rather than have events silently dropped. Default writes a
// short notice to os.Stderr.
func WithErrorHandler(fn func(error)) Option {
	return func(l *Logger) { l.onErr = fn }
}

// New returns a Logger that emits to out. Defaults: LevelInfo, time.Now,
// stderr error handler.
func New(out io.Writer, opts ...Option) *Logger {
	l := &Logger{
		out:   out,
		level: LevelInfo,
		now:   time.Now,
		onErr: func(err error) { _, _ = fmt.Fprintf(os.Stderr, "jsonlog: write error: %v\n", err) },
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

func (l *Logger) Debug(msg string, attrs map[string]any) { l.write(LevelDebug, msg, attrs) }
func (l *Logger) Info(msg string, attrs map[string]any)  { l.write(LevelInfo, msg, attrs) }
func (l *Logger) Warn(msg string, attrs map[string]any)  { l.write(LevelWarn, msg, attrs) }
func (l *Logger) Error(msg string, attrs map[string]any) { l.write(LevelError, msg, attrs) }

// Enabled reports whether an event at level would be emitted. Useful
// for skipping expensive attribute computation when the level is off.
func (l *Logger) Enabled(level Level) bool { return level >= l.level }

func (l *Logger) write(level Level, msg string, attrs map[string]any) {
	if level < l.level {
		return
	}
	entry := struct {
		Time  string         `json:"time"`
		Level string         `json:"level"`
		Msg   string         `json:"msg"`
		Attrs map[string]any `json:"attrs,omitempty"`
	}{
		Time:  l.now().UTC().Format(time.RFC3339Nano),
		Level: level.String(),
		Msg:   msg,
		Attrs: attrs,
	}
	// Marshal outside the lock so the reflect-driven map iteration
	// does not stall concurrent emitters; the critical section is the
	// single Write so access-log lines stay non-interleaved.
	buf, err := json.Marshal(entry)
	if err != nil {
		l.onErr(err)
		return
	}
	buf = append(buf, '\n')
	l.mu.Lock()
	_, err = l.out.Write(buf)
	l.mu.Unlock()
	if err != nil {
		l.onErr(err)
	}
}
