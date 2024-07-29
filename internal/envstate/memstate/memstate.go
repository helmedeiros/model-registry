// Package memstate is the in-memory envstate.Store backing used by
// tests and the v0.0.3 service-shell wiring. Writer methods return
// envstate.ErrNotImplemented; the Reader projection serves the empty
// state for any env until ADR-0005's lifecycle commits transitions.
package memstate

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/helmedeiros/model-registry/internal/envstate"
	"github.com/helmedeiros/model-registry/internal/store"
)

// Store is the in-memory envstate backing.
type Store struct {
	mu      sync.RWMutex
	state   map[string]envstate.State
	history map[string][]envstate.Transition
	clock   func() time.Time
}

// Option configures a Store at construction.
type Option func(*Store)

// WithClock injects a clock for deterministic timestamps in tests.
// Default is time.Now.
func WithClock(c func() time.Time) Option {
	return func(s *Store) { s.clock = c }
}

// New returns an empty memstate Store.
func New(opts ...Option) *Store {
	s := &Store{
		state:   map[string]envstate.State{},
		history: map[string][]envstate.Transition{},
		clock:   time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Get implements envstate.Reader. Returns the empty state for an env
// that has never been touched (per ADR-0004's "no 404 on unknown env"
// rule). The returned State is a value copy with cloned Role pointers
// so a future Writer mutating an entry in place cannot bleed through
// the read seam.
func (s *Store) Get(_ context.Context, env string) (envstate.State, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if st, ok := s.state[env]; ok {
		return cloneState(st), nil
	}
	return envstate.State{Env: env}, nil
}

func cloneState(st envstate.State) envstate.State {
	out := envstate.State{Env: st.Env, UpdatedAt: st.UpdatedAt}
	if st.Champion != nil {
		c := *st.Champion
		out.Champion = &c
	}
	if st.Challenger != nil {
		c := *st.Challenger
		out.Challenger = &c
	}
	return out
}

// History implements envstate.Reader. Pages transitions newest-first;
// an unknown cursor restarts traversal from the head.
func (s *Store) History(_ context.Context, env string, opts envstate.ListOptions) (envstate.HistoryPage, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = envstate.DefaultListLimit
	}
	if limit > envstate.MaxListLimit {
		limit = envstate.MaxListLimit
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	items := append([]envstate.Transition(nil), s.history[env]...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].At.Equal(items[j].At) {
			return string(items[i].ToHash) < string(items[j].ToHash)
		}
		return items[i].At.After(items[j].At)
	})

	start := 0
	if opts.Cursor != "" {
		for i, t := range items {
			if cursorOf(t) == opts.Cursor {
				start = i + 1
				break
			}
		}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}

	page := envstate.HistoryPage{Items: append([]envstate.Transition(nil), items[start:end]...)}
	if end < len(items) {
		page.NextCursor = cursorOf(items[end-1])
	}
	return page, nil
}

// cursorOf returns a stable opaque cursor identifying a transition.
func cursorOf(t envstate.Transition) string {
	return t.At.UTC().Format(time.RFC3339Nano) + "|" + string(t.ToHash)
}

// PromoteChampion implements envstate.Writer (stub for v0.0.3).
func (s *Store) PromoteChampion(_ context.Context, _ string, _ store.Hash, _, _ string) error {
	return envstate.ErrNotImplemented
}

// RollbackChampion implements envstate.Writer (stub for v0.0.3).
func (s *Store) RollbackChampion(_ context.Context, _ string, _, _ string) error {
	return envstate.ErrNotImplemented
}

// PromoteChallenger implements envstate.Writer (stub for v0.0.3).
func (s *Store) PromoteChallenger(_ context.Context, _ string, _ store.Hash, _, _ string) error {
	return envstate.ErrNotImplemented
}

// RejectChallenger implements envstate.Writer (stub for v0.0.3).
func (s *Store) RejectChallenger(_ context.Context, _ string, _, _ string) error {
	return envstate.ErrNotImplemented
}

