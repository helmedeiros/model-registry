// Package store is the typed Go substrate for the Versioned Config Store
// (ADR-0002).
package store

import "context"

// Reader is the read-only projection of the Store. HTTP read endpoints,
// the deploy fast-path, and any caller that does not mutate depend on
// Reader to keep test doubles narrow.
type Reader interface {
	// GetBundle returns the metadata-only bundle for a hash. Returns
	// ErrNotFound when the hash is unknown. No member bytes are loaded.
	GetBundle(ctx context.Context, h Hash) (Bundle, error)

	// GetMember returns the bytes of a single bundle member. Returns
	// ErrNotFound when the hash is unknown, ErrMemberAbsent when the
	// hash exists but the requested member was never uploaded, and
	// ErrInvalidKind when MemberKind is not one of this package's
	// constants. The returned ContentType is the artifact's declared
	// type for MemberSource and ContentTypeUnknown for derived members;
	// callers branch on MemberKind, not ContentType, to interpret bytes.
	GetMember(ctx context.Context, h Hash, m MemberKind) ([]byte, ContentType, error)

	// List paginates artifact summaries in created_at descending order,
	// with hash ascending as the tiebreaker. An unknown cursor restarts
	// from the beginning (backings do not validate cursors).
	List(ctx context.Context, opts ListOptions) (Page, error)

	// ResolveTag returns the current hash for a tag. Returns ErrTagUnknown
	// when the tag has never been assigned.
	ResolveTag(ctx context.Context, tag string) (Hash, error)

	// ListTags returns current tag-to-hash mappings (heads only). Tag
	// history is recorded by the backing but is not surfaced through
	// Reader; audit queries use a separate read model. Allocates one
	// map entry per registered tag — not for polling loops.
	ListTags(ctx context.Context) (map[string]Hash, error)
}

// Writer is the mutating projection of the Store.
type Writer interface {
	// Put writes a new artifact bundle and returns the assigned hash
	// (sha256 of SourceBytes). Repeated Put of the same source bytes is
	// idempotent: the existing hash is returned and SnapshotBytes,
	// DiagnoseBytes, and Metadata in the request are ignored. State on
	// the first successful Put is StateStaged.
	Put(ctx context.Context, req PutRequest) (Hash, error)

	// Tag points a label at a hash and is the sole substrate-level
	// activator: if the target is StateStaged it transitions to
	// StateActive. Re-pointing is allowed and appended to tag history.
	// Returns ErrNotFound when the hash is unknown, ErrInvalidTransition
	// when the target is StateDeprecated.
	Tag(ctx context.Context, tag string, h Hash) error

	// Deprecate marks an artifact StateDeprecated. The source may be
	// StateStaged or StateActive — both transition forward. Terminal:
	// the compliance-retention posture forecloses re-activation. Returns
	// ErrInvalidTransition when the artifact is already deprecated and
	// ErrNotFound when the hash is unknown.
	Deprecate(ctx context.Context, h Hash, reason string) error
}

// Store is the union for callers that need both projections.
type Store interface {
	Reader
	Writer
}

// List pagination policy. Shared by every backing so the contract's
// "callers branch on NextCursor" guarantee holds regardless of which
// backing is plugged in.
const (
	// DefaultListLimit applies when ListOptions.Limit is zero or negative.
	DefaultListLimit = 100
	// MaxListLimit caps any caller-supplied Limit silently to keep pages
	// bounded across backings.
	MaxListLimit = 1000
)
