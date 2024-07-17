//go:build e2e

// Package e2e holds the executable end-to-end proof that the user-facing
// capability remains green: an artifact written through the fsstore
// substrate, fetched back via GetMember, pushed to markup-svc via the
// body-based /admin/reload contract, and reflected in /decide. The bar
// is pre-registered in scientific/v0.0.1/REPORT.md.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/store"
	"github.com/helmedeiros/model-registry/internal/store/fsstore"
)

func markupSvcURL() string {
	if v := os.Getenv("MARKUP_SVC_URL"); v != "" {
		return v
	}
	return "http://localhost:8080"
}

func TestE2EBodyPush_RoundTrip(t *testing.T) {
	const totalWallBar = 250 * time.Millisecond

	svc := markupSvcURL()
	requireHealth(t, svc)
	t.Cleanup(func() { restoreFromDisk(t, svc) })

	start := time.Now()

	s, err := fsstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("fsstore.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	csv := []byte(
		"name,condition,factor,priority\n" +
			"e2e_canary,customer_tier == 'enterprise',1.93,99\n",
	)
	h, err := s.Put(context.Background(), store.PutRequest{
		SourceBytes: csv,
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedBy: "e2e"},
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	gotCSV, ct, err := s.GetMember(context.Background(), h, store.MemberSource)
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if string(gotCSV) != string(csv) {
		t.Fatalf("substrate round-trip changed bytes")
	}
	if ct != store.ContentTypeCSV {
		t.Fatalf("substrate dropped ContentType: %q", ct)
	}

	pushBody(t, svc, gotCSV, string(ct))

	decide := decideEnterprise(t, svc)
	if decide.Rule != "e2e_canary" {
		t.Fatalf("rule mismatch: got %q want e2e_canary", decide.Rule)
	}
	if decide.MarkupFactor != 1.93 {
		t.Fatalf("factor mismatch: got %v want 1.93", decide.MarkupFactor)
	}

	elapsed := time.Since(start)
	if elapsed > totalWallBar {
		t.Fatalf("E2E wall %s exceeded bar %s", elapsed, totalWallBar)
	}
	t.Logf("E2E PASS in %s (bar %s)", elapsed, totalWallBar)
}

func requireHealth(t *testing.T, svc string) {
	t.Helper()
	resp, err := http.Get(svc + "/healthz")
	if err != nil {
		t.Skipf("markup-svc at %s not reachable (%v) — skip", svc, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("markup-svc at %s healthz=%d — skip", svc, resp.StatusCode)
	}
}

func pushBody(t *testing.T, svc string, body []byte, ct string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, svc+"/admin/reload", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build reload req: %v", err)
	}
	req.Header.Set("Content-Type", ct)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("reload POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("reload http=%d body=%s", resp.StatusCode, raw)
	}
}

type decideResp struct {
	MarkupFactor float64 `json:"markup_factor"`
	Rule         string  `json:"rule"`
}

func decideEnterprise(t *testing.T, svc string) decideResp {
	t.Helper()
	body := []byte(`{"customer_tier":"enterprise","amount":100.0}`)
	req, err := http.NewRequest(http.MethodPost, svc+"/decide", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build decide req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("decide POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("decide http=%d body=%s", resp.StatusCode, raw)
	}
	var d decideResp
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatalf("decode decide: %v", err)
	}
	return d
}

func restoreFromDisk(t *testing.T, svc string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, svc+"/admin/reload", nil)
	if err != nil {
		t.Logf("restore build req: %v", err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("restore POST: %v", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Logf("restore http=%d body=%s", resp.StatusCode, raw)
	}
}

