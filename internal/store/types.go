package store

import "time"

// Hash is the hex-encoded SHA-256 of an artifact's source bytes.
type Hash string

// ContentType is the declared media type of an artifact's source bytes.
// The substrate does not validate that bytes match the declared type;
// that is the consumer's responsibility per ADR-0001.
type ContentType string

const (
	ContentTypeCSV      ContentType = "text/csv"
	ContentTypeSnapshot ContentType = "application/json"
	// ContentTypeUnknown is the sentinel returned by GetMember for
	// derived members; callers branch on MemberKind to interpret bytes.
	ContentTypeUnknown ContentType = ""
)

// MemberKind enumerates the addressable members of an artifact bundle.
// New derived formats land as additional constants, not new methods.
type MemberKind string

const (
	MemberSource   MemberKind = "source"
	MemberSnapshot MemberKind = "snapshot"
	MemberDiagnose MemberKind = "diagnose"
)

// State is the artifact lifecycle. Transitions are linear:
// StateStaged -> StateActive -> StateDeprecated. Deprecation is terminal.
type State string

const (
	StateStaged     State = "staged"
	StateActive     State = "active"
	StateDeprecated State = "deprecated"
)

// Metadata is the operator-facing description of an artifact bundle.
// DerivedByVersion identifies the producer of derived members (e.g. the
// toolchain version that compiled a snapshot from source); the substrate
// stores the value without interpretation.
type Metadata struct {
	CreatedAt        time.Time `json:"created_at"`
	CreatedBy        string    `json:"created_by"`
	SourceCommitSHA  string    `json:"source_commit_sha,omitempty"`
	Description      string    `json:"description,omitempty"`
	DerivedByVersion string    `json:"derived_by_version,omitempty"`
}

// PutRequest carries the bytes and metadata for a new artifact upload.
// SourceBytes and ContentType are required; SnapshotBytes, DiagnoseBytes,
// and Metadata are optional and ignored on re-Put of an existing hash.
type PutRequest struct {
	SourceBytes   []byte
	ContentType   ContentType
	SnapshotBytes []byte
	DiagnoseBytes []byte
	Metadata      Metadata
}

// Validate enforces the required-field invariants every backing depends on.
// Backings call it once at the top of Put before any I/O.
func (r PutRequest) Validate() error {
	if len(r.SourceBytes) == 0 {
		return ErrSourceRequired
	}
	if r.ContentType == ContentTypeUnknown {
		return ErrContentTypeRequired
	}
	return nil
}

// Bundle is the metadata-only projection of an artifact returned by
// GetBundle. Member bytes are fetched separately via GetMember so callers
// pay only for the bytes they read.
type Bundle struct {
	Hash        Hash
	ContentType ContentType
	Metadata    Metadata
	State       State
	HasSnapshot bool
	HasDiagnose bool
}

// Summary is the cheap-to-list projection of an artifact returned by List.
// Metadata is inlined by value; a Page with N Summary items copies N
// Metadata structs. Acceptable at the < 50 ms List bar but not a slot
// for kilo-QPS iteration.
type Summary struct {
	Hash        Hash
	ContentType ContentType
	Metadata    Metadata
	State       State
}

// ListOptions paginate the artifact list. An empty State means any state.
type ListOptions struct {
	Limit  int
	Cursor string
	State  State
}

// Page is one slice of a list traversal. An empty NextCursor means the
// list is exhausted.
type Page struct {
	Items      []Summary
	NextCursor string
}
