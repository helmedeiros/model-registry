// Package memstore is an in-memory implementation of store.Store. Not
// intended for production: state lives in process memory and is lost on
// restart.
package memstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/helmedeiros/model-registry/internal/store"
)

const (
	defaultListLimit = 100
	maxListLimit     = 1000
)

// Store is an in-memory implementation of store.Store.
type Store struct {
	mu      sync.RWMutex
	objects map[store.Hash]*artifact
	tags    map[string]store.Hash
	// tagHistory is append-only. Never read back through the Store
	// interface in v1; recorded so a future audit read-model has a source
	// of truth. Memstore is test-scoped and short-lived so no cap is
	// enforced; a durable backing must put history behind its real audit
	// table, not in an unbounded slice.
	tagHistory []tagEvent
	clock      func() time.Time
}

type artifact struct {
	hash             store.Hash
	state            store.State
	contentType      store.ContentType
	source           []byte
	snapshot         []byte
	diagnose         []byte
	metadata         store.Metadata
	createdAt        time.Time
	deprecatedAt     time.Time
	deprecatedReason string
}

type tagEvent struct {
	tag        string
	hash       store.Hash
	assignedAt time.Time
}

// Option configures a Store at construction.
type Option func(*Store)

// WithClock injects a clock for deterministic testing.
func WithClock(c func() time.Time) Option {
	return func(s *Store) { s.clock = c }
}

// New returns a fresh in-memory Store.
func New(opts ...Option) *Store {
	s := &Store{
		objects: make(map[store.Hash]*artifact),
		tags:    make(map[string]store.Hash),
		clock:   time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

var _ store.Store = (*Store)(nil)

// Put implements store.Writer.
func (s *Store) Put(_ context.Context, req store.PutRequest) (store.Hash, error) {
	if err := req.Validate(); err != nil {
		return "", err
	}
	h := hashOf(req.SourceBytes)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.objects[h]; ok {
		return h, nil
	}
	md := req.Metadata
	if md.CreatedAt.IsZero() {
		md.CreatedAt = s.clock()
	}
	s.objects[h] = &artifact{
		hash:        h,
		state:       store.StateStaged,
		contentType: req.ContentType,
		source:      cloneBytes(req.SourceBytes),
		snapshot:    cloneBytes(req.SnapshotBytes),
		diagnose:    cloneBytes(req.DiagnoseBytes),
		metadata:    md,
		createdAt:   md.CreatedAt,
	}
	return h, nil
}

// GetBundle implements store.Reader.
func (s *Store) GetBundle(_ context.Context, h store.Hash) (store.Bundle, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.objects[h]
	if !ok {
		return store.Bundle{}, store.ErrNotFound
	}
	return store.Bundle{
		Hash:        a.hash,
		ContentType: a.contentType,
		Metadata:    a.metadata,
		State:       a.state,
		HasSnapshot: len(a.snapshot) > 0,
		HasDiagnose: len(a.diagnose) > 0,
	}, nil
}

// GetMember implements store.Reader.
func (s *Store) GetMember(_ context.Context, h store.Hash, m store.MemberKind) ([]byte, store.ContentType, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.objects[h]
	if !ok {
		return nil, "", store.ErrNotFound
	}
	switch m {
	case store.MemberSource:
		return cloneBytes(a.source), a.contentType, nil
	case store.MemberSnapshot:
		if len(a.snapshot) == 0 {
			return nil, "", store.ErrMemberAbsent
		}
		return cloneBytes(a.snapshot), store.ContentTypeUnknown, nil
	case store.MemberDiagnose:
		if len(a.diagnose) == 0 {
			return nil, "", store.ErrMemberAbsent
		}
		return cloneBytes(a.diagnose), store.ContentTypeUnknown, nil
	default:
		return nil, "", store.ErrInvalidKind
	}
}

// List implements store.Reader.
func (s *Store) List(_ context.Context, opts store.ListOptions) (store.Page, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]*artifact, 0, len(s.objects))
	for _, a := range s.objects {
		if opts.State != "" && a.state != opts.State {
			continue
		}
		items = append(items, a)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].createdAt.Equal(items[j].createdAt) {
			return items[i].hash < items[j].hash
		}
		return items[i].createdAt.After(items[j].createdAt)
	})

	start := 0
	if opts.Cursor != "" {
		for i, a := range items {
			if string(a.hash) == opts.Cursor {
				start = i + 1
				break
			}
		}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}

	out := store.Page{Items: make([]store.Summary, 0, end-start)}
	for _, a := range items[start:end] {
		out.Items = append(out.Items, store.Summary{
			Hash:        a.hash,
			ContentType: a.contentType,
			Metadata:    a.metadata,
			State:       a.state,
		})
	}
	// Exclusive cursor: NextCursor names the last item returned on this
	// page. The next call's cursor lookup matches it and resumes at i+1.
	if end < len(items) {
		out.NextCursor = string(items[end-1].hash)
	}
	return out, nil
}

// Tag implements store.Writer.
func (s *Store) Tag(_ context.Context, tag string, h store.Hash) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.objects[h]
	if !ok {
		return store.ErrNotFound
	}
	if a.state == store.StateDeprecated {
		return store.ErrInvalidTransition
	}
	if a.state == store.StateStaged {
		a.state = store.StateActive
	}
	s.tags[tag] = h
	s.tagHistory = append(s.tagHistory, tagEvent{tag: tag, hash: h, assignedAt: s.clock()})
	return nil
}

// ResolveTag implements store.Reader.
func (s *Store) ResolveTag(_ context.Context, tag string) (store.Hash, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.tags[tag]
	if !ok {
		return "", store.ErrTagUnknown
	}
	return h, nil
}

// ListTags implements store.Reader. Defensive copy prevents callers from
// mutating the internal map; cost is O(len(tags)) under RLock.
func (s *Store) ListTags(_ context.Context) (map[string]store.Hash, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]store.Hash, len(s.tags))
	for k, v := range s.tags {
		out[k] = v
	}
	return out, nil
}

// Deprecate implements store.Writer.
func (s *Store) Deprecate(_ context.Context, h store.Hash, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.objects[h]
	if !ok {
		return store.ErrNotFound
	}
	if a.state == store.StateDeprecated {
		return store.ErrInvalidTransition
	}
	a.state = store.StateDeprecated
	a.deprecatedAt = s.clock()
	a.deprecatedReason = reason
	return nil
}

func hashOf(b []byte) store.Hash {
	sum := sha256.Sum256(b)
	return store.Hash(hex.EncodeToString(sum[:]))
}

// cloneBytes returns an independent copy so the Store retains no
// reference to the caller's slice on Put and returns no reference to the
// Store's slice on Get.
func cloneBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
