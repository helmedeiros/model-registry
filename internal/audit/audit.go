// Package audit is the append-only operator action log (ADR-0004).
// Reader.List returns recent entries newest-first; Writer.Record
// appends. ADR-0005's lifecycle endpoints land the real Record
// implementations on the same typed contract.
package audit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/helmedeiros/model-registry/internal/store"
)

// Entry is one operator action recorded in the audit log. ID is a
// ULID (lexicographically sortable, monotonic per-time-bucket); the
// memaudit + future fsaudit sort tiebreakers depend on this so a
// non-ULID seeded entry would silently corrupt newest-first order.
// TraceID is the W3C trace id of the request that drove the action,
// blank when the action was minted without a trace context. Operators
// hop from "who did this?" in /audit to the matching Jaeger waterfall.
type Entry struct {
	ID           string
	Operator     string
	Action       string
	Target       string
	ArtifactHash store.Hash
	Reason       string
	At           time.Time
	TraceID      string
}

// ListOptions paginate audit queries. The shape is intentionally
// mirrored from envstate.ListOptions + store.ListOptions — each
// domain owns its pagination policy so the constants below can evolve
// independently of the substrate's.
type ListOptions struct {
	Limit  int
	Cursor string
}

// Page is one paginated slice of the audit log.
type Page struct {
	Items      []Entry
	NextCursor string
}

// Reader is the read-only projection used by GET /audit.
type Reader interface {
	List(ctx context.Context, opts ListOptions) (Page, error)
}

// Writer appends one entry. ADR-0005's lifecycle endpoints land
// implementations; v0.0.3 backings return ErrNotImplemented.
type Writer interface {
	Record(ctx context.Context, entry Entry) error
}

// Store is the union for callers that need both projections.
type Store interface {
	Reader
	Writer
}

// ErrNotImplemented wraps errors.ErrUnsupported so callers can use
// `errors.Is(err, errors.ErrUnsupported)` to detect the missing
// projection without importing this package. Retained for future
// Writer slots that may not ship implementations together with the
// rest of the projection.
var ErrNotImplemented = fmt.Errorf("audit: writer not implemented: %w", errors.ErrUnsupported)

// Record validation sentinels — returned by Writer.Record when the
// minimum-meaningful audit-entry fields are missing. A non-validated
// entry would silently devalue the audit trail; each Record call must
// carry enough context to answer "who, what, where, when".
var (
	ErrIDRequired       = errors.New("audit: id required")
	ErrOperatorRequired = errors.New("audit: operator required")
	ErrActionRequired   = errors.New("audit: action required")
	ErrTargetRequired   = errors.New("audit: target required")
	ErrAtRequired       = errors.New("audit: at required")
	ErrDuplicateID      = errors.New("audit: duplicate id")
)

// Pagination policy.
const (
	DefaultListLimit = 100
	MaxListLimit     = 1000
)
