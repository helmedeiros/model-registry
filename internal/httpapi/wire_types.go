package httpapi

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
}

type AuditPage struct {
	Items      []AuditEntryView `json:"items"`
	NextCursor string           `json:"next_cursor,omitempty"`
}
