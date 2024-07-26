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
