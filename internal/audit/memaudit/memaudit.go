// Package memaudit is the in-memory audit.Store backing used by tests
// and the v0.0.3 service-shell wiring.
package memaudit

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/helmedeiros/model-registry/internal/audit"
)

// Store is the in-memory audit backing.
type Store struct {
	mu      sync.RWMutex
	entries []audit.Entry
	clock   func() time.Time
}

// Option configures a Store at construction.
type Option func(*Store)

// WithClock injects a clock for deterministic timestamps in tests.
func WithClock(c func() time.Time) Option {
	return func(s *Store) { s.clock = c }
}

// New returns an empty memaudit Store.
func New(opts ...Option) *Store {
	s := &Store{clock: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// List implements audit.Reader. Pages newest-first; an unknown cursor
// restarts traversal from the head.
func (s *Store) List(_ context.Context, opts audit.ListOptions) (audit.Page, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = audit.DefaultListLimit
	}
	if limit > audit.MaxListLimit {
		limit = audit.MaxListLimit
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	items := append([]audit.Entry(nil), s.entries...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].At.Equal(items[j].At) {
			return items[i].ID > items[j].ID
		}
		return items[i].At.After(items[j].At)
	})

	start := 0
	if opts.Cursor != "" {
		for i, e := range items {
			if cursorOf(e) == opts.Cursor {
				start = i + 1
				break
			}
		}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}

	page := audit.Page{Items: append([]audit.Entry(nil), items[start:end]...)}
	if end < len(items) {
		page.NextCursor = cursorOf(items[end-1])
	}
	return page, nil
}

func cursorOf(e audit.Entry) string {
	return e.At.UTC().Format(time.RFC3339Nano) + "|" + e.ID
}

// Record implements audit.Writer. Refuses duplicate IDs (ULID
// uniqueness is a construction invariant; a repeat signals a
// generator bug, not an idempotent retry) and serialises appends
// under WLock.
func (s *Store) Record(_ context.Context, entry audit.Entry) error {
	if entry.ID == "" {
		return audit.ErrIDRequired
	}
	if entry.Operator == "" {
		return audit.ErrOperatorRequired
	}
	if entry.Action == "" {
		return audit.ErrActionRequired
	}
	if entry.Target == "" {
		return audit.ErrTargetRequired
	}
	if entry.At.IsZero() {
		return audit.ErrAtRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.entries {
		if existing.ID == entry.ID {
			return audit.ErrDuplicateID
		}
	}
	s.entries = append(s.entries, entry)
	return nil
}
