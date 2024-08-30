package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/store"
)

// DefaultMaxUploadBytes caps the source part of POST /upload. ADR-0005
// commits a 16 MB ceiling — matches the upper end of the realistic
// rule-set size and gives operators a clear "uploaded too much" signal.
const DefaultMaxUploadBytes int64 = 16 << 20

// errUploadTooLarge is the internal sentinel readUploadParts returns
// when a part exceeds the configured MaxBytes. The handler translates
// it to 413; other readUploadParts errors map to 400.
var errUploadTooLarge = errors.New("upload_too_large")

// ULIDSource generates the audit-entry IDs the upload handler writes.
type ULIDSource interface {
	New() (string, error)
}

// UploadDeps is the subset of dependencies POST /upload needs.
// Substrate is the store.Store union (Reader + Writer) because the
// handler writes the artifact and then reads the bundle back to
// answer with state + has_snapshot/has_diagnose.
type UploadDeps struct {
	Substrate store.Store
	Audit     audit.Writer
	ULID      ULIDSource
	Logger    AccessSink
	Now       func() time.Time
	MaxBytes  int64
	Metrics   UploadMetrics
}

// Upload returns the POST /upload handler. Parses multipart/form-data
// per ADR-0005, calls store.Writer.Put, records an audit entry, and
// returns the UploadResponse JSON envelope.
func Upload(deps UploadDeps) http.Handler {
	if deps.Substrate == nil {
		panic("httpapi.Upload: Substrate is required")
	}
	if deps.Audit == nil {
		panic("httpapi.Upload: Audit is required")
	}
	if deps.ULID == nil {
		panic("httpapi.Upload: ULID is required")
	}
	if deps.Logger == nil {
		panic("httpapi.Upload: Logger is required")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.MaxBytes <= 0 {
		deps.MaxBytes = DefaultMaxUploadBytes
	}
	if deps.Metrics == nil {
		deps.Metrics = noopMetrics{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if err := r.ParseMultipartForm(deps.MaxBytes); err != nil {
			if errors.Is(err, multipart.ErrMessageTooLarge) {
				deps.Metrics.RecordUpload("too_large")
				writeError(w, http.StatusRequestEntityTooLarge, "upload_too_large")
				return
			}
			deps.Metrics.RecordUpload("invalid")
			writeError(w, http.StatusBadRequest, "invalid_multipart")
			return
		}

		parts, err := readUploadParts(r, deps.MaxBytes)
		if errors.Is(err, errUploadTooLarge) {
			deps.Metrics.RecordUpload("too_large")
			writeError(w, http.StatusRequestEntityTooLarge, "upload_too_large")
			return
		}
		if err != nil {
			deps.Metrics.RecordUpload("invalid")
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		hash, err := deps.Substrate.Put(r.Context(), store.PutRequest{
			SourceBytes:   parts.source,
			SnapshotBytes: parts.snapshot,
			DiagnoseBytes: parts.diagnose,
			ContentType:   parts.contentType,
			Metadata:      parts.metadata,
		})
		if err != nil {
			deps.Metrics.RecordUpload("substrate_error")
			writeError(w, http.StatusInternalServerError, "put_failed")
			return
		}

		bundle, err := deps.Substrate.GetBundle(r.Context(), hash)
		if err != nil {
			deps.Metrics.RecordUpload("substrate_error")
			writeError(w, http.StatusInternalServerError, "bundle_lookup_failed")
			return
		}

		auditCtx, auditSpan := startChildSpan(r.Context(), "registry.audit.record")
		if err := recordUpload(auditCtx, deps, hash, parts.operator); err != nil {
			// Put already committed; returning 500 would mislead the
			// caller into thinking the upload did not land. Log the
			// audit gap as a structured event so the observability
			// surface can alarm on it, then continue with the success
			// envelope.
			auditSpan.RecordError(err)
			logInfoWithTrace(deps.Logger, auditCtx, "registry.audit.write_failed", map[string]any{
				"action":        "upload",
				"artifact_hash": string(hash),
				"operator":      parts.operator,
				"error":         err.Error(),
			})
		}
		auditSpan.End()

		deps.Metrics.RecordUpload("ok")
		writeJSON(w, http.StatusOK, UploadResponse{
			Hash:     string(hash),
			State:    string(bundle.State),
			Diagnose: parts.diagnoseJSON(),
		})
	})
}

type uploadParts struct {
	source      []byte
	snapshot    []byte
	diagnose    []byte
	contentType store.ContentType
	metadata    store.Metadata
	operator    string
}

func (p uploadParts) diagnoseJSON() json.RawMessage {
	if len(p.diagnose) == 0 {
		return nil
	}
	return p.diagnose
}

func readUploadParts(r *http.Request, maxBytes int64) (uploadParts, error) {
	if r.MultipartForm == nil {
		return uploadParts{}, errors.New("invalid_multipart")
	}

	sourceFiles := r.MultipartForm.File["source"]
	if len(sourceFiles) == 0 {
		return uploadParts{}, errors.New("source_required")
	}
	sourceHeader := sourceFiles[0]
	sourceBytes, err := readFile(sourceHeader, maxBytes)
	if err != nil {
		return uploadParts{}, err
	}
	contentType := store.ContentType(sourceHeader.Header.Get("Content-Type"))
	if contentType == "" {
		return uploadParts{}, errors.New("unsupported_content_type")
	}
	switch contentType {
	case store.ContentTypeCSV, store.ContentTypeSnapshot:
	default:
		return uploadParts{}, errors.New("unsupported_content_type")
	}

	parts := uploadParts{
		source:      sourceBytes,
		contentType: contentType,
	}

	if files := r.MultipartForm.File["snapshot"]; len(files) > 0 {
		parts.snapshot, err = readFile(files[0], maxBytes)
		if err != nil {
			return uploadParts{}, err
		}
	}
	if files := r.MultipartForm.File["diagnose"]; len(files) > 0 {
		parts.diagnose, err = readFile(files[0], maxBytes)
		if err != nil {
			return uploadParts{}, err
		}
	}
	if files := r.MultipartForm.File["metadata"]; len(files) > 0 {
		metaBytes, err := readFile(files[0], maxBytes)
		if err != nil {
			return uploadParts{}, err
		}
		var wire UploadMetadata
		if err := json.Unmarshal(metaBytes, &wire); err != nil {
			return uploadParts{}, fmt.Errorf("invalid_metadata: %w", err)
		}
		parts.metadata = fromUploadMetadata(wire)
		parts.operator = wire.CreatedBy
	}
	if parts.operator == "" {
		parts.operator = "anonymous"
	}
	return parts, nil
}

func readFile(fh *multipart.FileHeader, maxBytes int64) ([]byte, error) {
	if fh.Size > maxBytes {
		return nil, errUploadTooLarge
	}
	f, err := fh.Open()
	if err != nil {
		return nil, fmt.Errorf("open part: %w", err)
	}
	defer func() { _ = f.Close() }()
	buf, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read part: %w", err)
	}
	if int64(len(buf)) > maxBytes {
		return nil, errUploadTooLarge
	}
	return buf, nil
}

func fromUploadMetadata(w UploadMetadata) store.Metadata {
	md := store.Metadata{
		CreatedBy:        w.CreatedBy,
		Description:      w.Description,
		SourceCommitSHA:  w.SourceCommitSHA,
		DerivedByVersion: w.DerivedByVersion,
	}
	if len(w.Rules) == 0 {
		return md
	}
	md.Rules = make([]store.RuleProvenance, len(w.Rules))
	for i, r := range w.Rules {
		md.Rules[i] = store.RuleProvenance{
			RuleID:          r.RuleID,
			Author:          r.Author,
			SourceCommitSHA: r.SourceCommitSHA,
			PRURL:           r.PRURL,
			Description:     r.Description,
			LastModified:    parseTimeOrZero(r.LastModified),
		}
	}
	return md
}

func parseTimeOrZero(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return t
}

func recordUpload(ctx context.Context, deps UploadDeps, hash store.Hash, operator string) error {
	id, err := deps.ULID.New()
	if err != nil {
		return err
	}
	return deps.Audit.Record(ctx, audit.Entry{
		ID:           id,
		Operator:     operator,
		Action:       "upload",
		Target:       "artifacts/" + string(hash),
		ArtifactHash: hash,
		At:           deps.Now(),
		TraceID:      traceIDFromCtx(ctx),
	})
}
