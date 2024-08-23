package httpapi

import "encoding/json"

// Wire envelopes for the read-only operator endpoints. Kept separate
// from internal store types so the public JSON shape is stable
// against fields landing on the internal structs.

type ArtifactSummary struct {
	Hash        string           `json:"hash"`
	ContentType string           `json:"content_type"`
	State       string           `json:"state"`
	Metadata    ArtifactMetaJSON `json:"metadata"`
}

type ArtifactBundle struct {
	Hash        string           `json:"hash"`
	ContentType string           `json:"content_type"`
	State       string           `json:"state"`
	Metadata    ArtifactMetaJSON `json:"metadata"`
	HasSnapshot bool             `json:"has_snapshot"`
	HasDiagnose bool             `json:"has_diagnose"`
}

type ArtifactMetaJSON struct {
	CreatedAt        string `json:"created_at"`
	CreatedBy        string `json:"created_by"`
	SourceCommitSHA  string `json:"source_commit_sha,omitempty"`
	Description      string `json:"description,omitempty"`
	DerivedByVersion string `json:"derived_by_version,omitempty"`
}

type ArtifactPage struct {
	Items      []ArtifactSummary `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

// --- env-state ---

type EnvRoleView struct {
	Hash       string `json:"hash"`
	PromotedBy string `json:"promoted_by"`
	PromotedAt string `json:"promoted_at"`
}

type EnvStateView struct {
	Env        string       `json:"env"`
	Champion   *EnvRoleView `json:"champion"`
	Challenger *EnvRoleView `json:"challenger"`
	// Pointer so a fresh-env response carries `"updated_at": null`
	// rather than an absent field — matches ADR-0004's envelope.
	UpdatedAt *string `json:"updated_at"`
}

type EnvTransitionView struct {
	Env      string `json:"env"`
	Kind     string `json:"kind"`
	FromHash string `json:"from_hash,omitempty"`
	ToHash   string `json:"to_hash,omitempty"`
	Operator string `json:"operator,omitempty"`
	Reason   string `json:"reason,omitempty"`
	At       string `json:"at"`
}

type EnvHistoryPage struct {
	Items      []EnvTransitionView `json:"items"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

// --- audit ---

// AuditEntryView's operator/action/target are always-present per
// ADR-0004; artifact_hash + reason are genuinely optional (a rollback
// without a stated reason is allowed).
type AuditEntryView struct {
	ID           string `json:"id"`
	Operator     string `json:"operator"`
	Action       string `json:"action"`
	Target       string `json:"target"`
	ArtifactHash string `json:"artifact_hash,omitempty"`
	Reason       string `json:"reason,omitempty"`
	At           string `json:"at"`
	// TraceID is the W3C trace id of the request that drove the action.
	// Operators paste this into Jaeger to reach the waterfall. Blank when
	// the entry was recorded outside a trace context.
	TraceID string `json:"trace_id,omitempty"`
}

type AuditPage struct {
	Items      []AuditEntryView `json:"items"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

// --- upload (ADR-0005) ---

type UploadResponse struct {
	Hash  string `json:"hash"`
	State string `json:"state"`
	// Diagnose is the raw bytes the caller uploaded in the diagnose
	// part, if any.
	Diagnose json.RawMessage `json:"diagnose,omitempty"`
}

// UploadMetadata is the JSON envelope a caller posts in the metadata
// part of /upload. Mirrors store.Metadata's wire-relevant fields but
// stays in the public wire layer so clients do not import
// internal/store.
type UploadMetadata struct {
	CreatedBy        string `json:"created_by,omitempty"`
	Description      string `json:"description,omitempty"`
	SourceCommitSHA  string `json:"source_commit_sha,omitempty"`
	DerivedByVersion string `json:"derived_by_version,omitempty"`
}

// --- promote (ADR-0005) ---

type PromoteRequest struct {
	Hash     string `json:"hash"`
	Env      string `json:"env"`
	Role     string `json:"role"`
	Operator string `json:"operator"`
	Reason   string `json:"reason,omitempty"`
}

type InstanceResultView struct {
	URL        string `json:"url"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

type DeployView struct {
	Instances []InstanceResultView `json:"instances"`
	Outcome   string               `json:"outcome"`
}

type PromoteResponse struct {
	Env          string     `json:"env"`
	PreviousHash string     `json:"previous_hash,omitempty"`
	NewHash      string     `json:"new_hash"`
	Deploy       DeployView `json:"deploy"`
}

// --- rollback (ADR-0005) ---

type RollbackRequest struct {
	Env      string `json:"env"`
	Operator string `json:"operator"`
	Reason   string `json:"reason"`
}

type RollbackResponse struct {
	Env          string     `json:"env"`
	PreviousHash string     `json:"previous_hash"`
	RolledTo     string     `json:"rolled_to"`
	Deploy       DeployView `json:"deploy"`
}
