// Package memstate is the in-memory envstate.Store backing.
// PromoteChampion and RollbackChampion implement the v0.0.4 champion
// lifecycle from ADR-0005; PromoteChallenger and RejectChallenger
// remain stubbed with envstate.ErrNotImplemented until ADR-0006.
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

// PromoteChampion implements envstate.Writer. Runs the entire
// snapshot-and-write sequence under a single WLock so a concurrent
// Promote cannot change the champion between the snapshot of the
// previous hash and the new write — ADR-0005's TOCTOU constraint.
// The clock read happens inside the lock so concurrent writes
// produce a monotonic At sequence regardless of the goroutine
// schedule.
func (s *Store) PromoteChampion(_ context.Context, env string, h store.Hash, operator, reason string) (store.Hash, error) {
	if env == "" {
		return "", envstate.ErrEnvRequired
	}
	if h == "" {
		return "", envstate.ErrHashRequired
	}
	if operator == "" {
		return "", envstate.ErrOperatorRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()

	previous := store.Hash("")
	if existing, ok := s.state[env]; ok && existing.Champion != nil {
		previous = existing.Champion.Hash
	}

	current := s.state[env]
	current.Env = env
	current.Champion = &envstate.Role{Hash: h, PromotedBy: operator, PromotedAt: now}
	current.UpdatedAt = now
	s.state[env] = current

	s.history[env] = append(s.history[env], envstate.Transition{
		Env:      env,
		Kind:     envstate.KindChampionPromoted,
		FromHash: previous,
		ToHash:   h,
		Operator: operator,
		Reason:   reason,
		At:       now,
	})
	return previous, nil
}

// RollbackChampion implements envstate.Writer. Restores the prior
// champion hash from history; the same WLock covers the snapshot-and-
// write so a concurrent Promote cannot reshape state mid-rollback.
// The clock read happens inside the lock for the same monotonic-
// timestamp reason PromoteChampion does.
func (s *Store) RollbackChampion(_ context.Context, env, operator, reason string) (store.Hash, error) {
	if env == "" {
		return "", envstate.ErrEnvRequired
	}
	if operator == "" {
		return "", envstate.ErrOperatorRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()

	existing, ok := s.state[env]
	if !ok || existing.Champion == nil {
		return "", envstate.ErrNoChampion
	}
	previousChampion := previousChampionHash(s.history[env], existing.Champion.Hash)
	if previousChampion == "" {
		return "", envstate.ErrNoPreviousChampion
	}

	rolledFrom := existing.Champion.Hash
	existing.Champion = &envstate.Role{Hash: previousChampion, PromotedBy: operator, PromotedAt: now}
	existing.UpdatedAt = now
	s.state[env] = existing

	s.history[env] = append(s.history[env], envstate.Transition{
		Env:      env,
		Kind:     envstate.KindChampionRolledBack,
		FromHash: rolledFrom,
		ToHash:   previousChampion,
		Operator: operator,
		Reason:   reason,
		At:       now,
	})
	return previousChampion, nil
}

// PreviousChampion implements envstate.Reader. Read-only equivalent of
// RollbackChampion's history walk — same lock posture (RLock here),
// same error semantics (ErrNoChampion / ErrNoPreviousChampion). The
// handler uses this to fetch the source bytes BEFORE the deploy fires
// so the rollback can be aborted cleanly if the previous artifact is
// missing.
func (s *Store) PreviousChampion(_ context.Context, env string) (store.Hash, error) {
	if env == "" {
		return "", envstate.ErrEnvRequired
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	existing, ok := s.state[env]
	if !ok || existing.Champion == nil {
		return "", envstate.ErrNoChampion
	}
	previous := previousChampionHash(s.history[env], existing.Champion.Hash)
	if previous == "" {
		return "", envstate.ErrNoPreviousChampion
	}
	return previous, nil
}

// previousChampionHash walks the transition log backwards to find the
// most recent ToHash promoted under KindChampionPromoted that is not
// the current champion. Returns empty when there is no such entry.
// Called under the caller's WLock so the scan is bounded CPU under
// lock; O(N) on per-env history depth, acceptable at operator
// cadence. The fsstate backing will replace this with an indexed
// query rather than reuse the linear walk.
func previousChampionHash(history []envstate.Transition, current store.Hash) store.Hash {
	for i := len(history) - 1; i >= 0; i-- {
		t := history[i]
		if t.Kind != envstate.KindChampionPromoted {
			continue
		}
		if t.ToHash != current {
			return t.ToHash
		}
	}
	return ""
}

// PromoteChallenger implements envstate.Writer. Stubbed until ADR-0006.
func (s *Store) PromoteChallenger(_ context.Context, _ string, _ store.Hash, _, _ string) error {
	return envstate.ErrNotImplemented
}

// RejectChallenger implements envstate.Writer. Stubbed until ADR-0006.
func (s *Store) RejectChallenger(_ context.Context, _ string, _, _ string) error {
	return envstate.ErrNotImplemented
}

