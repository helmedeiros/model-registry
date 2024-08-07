package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"os/user"
	"path/filepath"

	"github.com/helmedeiros/model-registry/internal/httpapi"
)

func runUpload(ctx context.Context, args []string, stdout, stderr io.Writer, c *http.Client) int {
	fs := flag.NewFlagSet("upload", flag.ContinueOnError)
	common := registerCommonFlags(fs)
	sourcePath := fs.String("file", "", "path to source CSV (required)")
	snapshotPath := fs.String("snapshot", "", "optional path to bre-go snapshot JSON")
	diagnosePath := fs.String("diagnose", "", "optional path to bre-go diagnose JSON")
	operator := fs.String("operator", defaultOperator(), "operator identity for the audit trail")
	description := fs.String("description", "", "free-form description recorded with the artifact")
	commitSHA := fs.String("source-commit-sha", "", "optional source repo commit SHA")
	derivedBy := fs.String("derived-by-version", "", "optional bre-go version that produced the snapshot")
	if code, ok := parseFlags(fs, args, stderr); !ok {
		return code
	}
	if *sourcePath == "" {
		fmt.Fprintln(stderr, "usage: mrctl upload --file <path> [--snapshot <path>] [--diagnose <path>]")
		return 2
	}

	body, contentType, err := buildUploadBody(*sourcePath, *snapshotPath, *diagnosePath, httpapi.UploadMetadata{
		CreatedBy:        *operator,
		Description:      *description,
		SourceCommitSHA:  *commitSHA,
		DerivedByVersion: *derivedBy,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	var resp httpapi.UploadResponse
	if _, err := postJSON(ctx, c, common.registry, "/upload", body, contentType, &resp); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if common.jsonOut {
		return emitJSON(stdout, stderr, resp)
	}
	fmt.Fprintf(stdout, "hash:        %s\n", resp.Hash)
	fmt.Fprintf(stdout, "state:       %s\n", resp.State)
	if len(resp.Diagnose) > 0 {
		fmt.Fprintln(stdout, "diagnose:    (returned by registry)")
	}
	return 0
}

// buildUploadBody assembles the multipart payload. Each part is read
// fully into memory before the request is sent — at the 16 MB
// per-part ceiling the peak heap is roughly 3 × MaxBytes (source +
// snapshot + diagnose). For larger uploads switch to io.Pipe and
// stream parts concurrently with the POST.
func buildUploadBody(sourcePath, snapshotPath, diagnosePath string, md httpapi.UploadMetadata) (io.Reader, string, error) {
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)

	if err := writeFilePart(w, "source", sourcePath, "text/csv"); err != nil {
		return nil, "", err
	}
	if snapshotPath != "" {
		if err := writeFilePart(w, "snapshot", snapshotPath, "application/json"); err != nil {
			return nil, "", err
		}
	}
	if diagnosePath != "" {
		if err := writeFilePart(w, "diagnose", diagnosePath, "application/json"); err != nil {
			return nil, "", err
		}
	}
	if md != (httpapi.UploadMetadata{}) {
		mdBytes, err := json.Marshal(md)
		if err != nil {
			return nil, "", fmt.Errorf("encode metadata: %w", err)
		}
		hdr := textproto.MIMEHeader{}
		hdr.Set("Content-Disposition", `form-data; name="metadata"; filename="metadata.json"`)
		hdr.Set("Content-Type", "application/json")
		mw, err := w.CreatePart(hdr)
		if err != nil {
			return nil, "", fmt.Errorf("metadata part: %w", err)
		}
		if _, err := mw.Write(mdBytes); err != nil {
			return nil, "", fmt.Errorf("write metadata: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", fmt.Errorf("close multipart: %w", err)
	}
	return buf, w.FormDataContentType(), nil
}

func writeFilePart(w *multipart.Writer, name, path, contentType string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, name, filepath.Base(path)))
	hdr.Set("Content-Type", contentType)
	mw, err := w.CreatePart(hdr)
	if err != nil {
		return fmt.Errorf("%s part: %w", name, err)
	}
	if _, err := io.Copy(mw, f); err != nil {
		return fmt.Errorf("copy %s: %w", name, err)
	}
	return nil
}

func defaultOperator() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}
