//go:build e2e

// Live-stack end-to-end proof for the ADR-0005 champion lifecycle.
// Boots the full model-registry in-process, points it at a running
// markup-svc via static instance discovery, then drives the operator
// surface end-to-end:
//
//	POST /upload  csvA  -> hashA
//	POST /promote hashA -> rolling-push to markup-svc /admin/reload
//	POST /decide  (markup-svc)        -> asserts csvA rule fires
//	POST /upload  csvB  -> hashB
//	POST /promote hashB -> rolling-push to markup-svc /admin/reload
//	POST /decide                       -> asserts csvB rule fires
//	POST /rollback     -> rolling-push back to csvA
//	POST /decide                       -> asserts csvA rule fires again
//	GET  /audit        -> asserts the three transitions landed
//
// Build-tagged e2e so default `make test` does not need a running
// markup-svc. Operators run with `make e2e` against a live stack;
// MARKUP_SVC_URL overrides the default http://localhost:8080.
//
// This is the integration proof ADR-0005 §Status names as the gate
// for the next ADR-0005.x revision.
package v0_0_4

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/audit/memaudit"
	"github.com/helmedeiros/model-registry/internal/deployer/rolling"
	"github.com/helmedeiros/model-registry/internal/envstate/memstate"
	"github.com/helmedeiros/model-registry/internal/httpapi"
	"github.com/helmedeiros/model-registry/internal/instances/static"
	"github.com/helmedeiros/model-registry/internal/observability/metrics/prom"
	"github.com/helmedeiros/model-registry/internal/store/memstore"
	"github.com/helmedeiros/model-registry/internal/ulid"
)

const (
	csvA = "name,condition,factor,priority\n" +
		"e2e_lifecycle_a,customer_tier == 'enterprise',1.41,99\n"
	csvB = "name,condition,factor,priority\n" +
		"e2e_lifecycle_b,customer_tier == 'enterprise',2.71,99\n"
)

func TestE2ELifecycle_RoundTrip(t *testing.T) {
	svc := markupSvcURL()
	requireHealth(t, svc)
	t.Cleanup(func() { restoreFromDisk(t, svc) })

	reg := bootRegistry(t, svc)
	t.Cleanup(reg.Close)

	hashA := uploadCSV(t, reg.URL, []byte(csvA))
	promote(t, reg.URL, hashA)
	if got := decideRule(t, svc); got != "e2e_lifecycle_a" {
		t.Fatalf("after promote A: rule=%q want e2e_lifecycle_a", got)
	}

	hashB := uploadCSV(t, reg.URL, []byte(csvB))
	promote(t, reg.URL, hashB)
	if got := decideRule(t, svc); got != "e2e_lifecycle_b" {
		t.Fatalf("after promote B: rule=%q want e2e_lifecycle_b", got)
	}

	rollback(t, reg.URL)
	if got := decideRule(t, svc); got != "e2e_lifecycle_a" {
		t.Fatalf("after rollback: rule=%q want e2e_lifecycle_a", got)
	}

	page := listAudit(t, reg.URL)
	actions := make([]string, 0, len(page.Items))
	for _, e := range page.Items {
		actions = append(actions, e.Action)
	}
	// Newest-first time-ordered sequence: upload-A → promote-A →
	// upload-B → promote-B → rollback. Newest at index 0.
	wantActions := []string{"rollback", "promote", "upload", "promote", "upload"}
	if len(actions) != len(wantActions) {
		t.Fatalf("audit entries=%d want %d: %v", len(actions), len(wantActions), actions)
	}
	for i, want := range wantActions {
		if actions[i] != want {
			t.Fatalf("audit[%d]=%q want %q (full: %v)", i, actions[i], want, actions)
		}
	}

	// --- Scientific verification of the observability surface ---
	assertLifecycleSpansEmitted(t, reg)
	assertAuditEntriesCarryTraceID(t, reg)
	assertDeployDurationExemplarsPresent(t, reg)
}

// assertLifecycleSpansEmitted proves the lifecycle child spans landed
// for every handler call the round-trip exercised. Without these
// assertions a future refactor that drops a tracer.Start would
// silently regress operator visibility in Jaeger.
func assertLifecycleSpansEmitted(t *testing.T, reg registryHandle) {
	t.Helper()
	want := map[string]int{
		"registry.champion.commit_state":   3, // promote-A + promote-B + rollback
		"registry.audit.record":            5, // upload-A + upload-B + promote-A + promote-B + rollback
		"registry.deploy.push_to_instance": 3, // 1 instance × 3 deploys (promote-A + promote-B + rollback)
		"registry.deploy.readyz":           3, // 1 readyz poll per push
	}
	got := map[string]int{}
	for _, s := range reg.spans.Ended() {
		got[s.Name()]++
	}
	for name, n := range want {
		if got[name] < n {
			t.Errorf("span %q count=%d want >=%d", name, got[name], n)
		}
	}
	if t.Failed() {
		t.Logf("recorded spans: %v", got)
	}
}

// assertAuditEntriesCarryTraceID proves every audit row has a
// non-empty TraceID. The /audit JSON endpoint then carries this
// through wire_types.AuditEntryView.TraceID so operators hop from
// the ledger to Jaeger in two clicks.
func assertAuditEntriesCarryTraceID(t *testing.T, reg registryHandle) {
	t.Helper()
	page, err := reg.audit.List(context.Background(), audit.ListOptions{Limit: 100})
	if err != nil {
		t.Fatalf("audit.List: %v", err)
	}
	for i, e := range page.Items {
		if e.TraceID == "" {
			t.Errorf("audit[%d] action=%q has empty TraceID — operator cannot hop to Jaeger", i, e.Action)
		}
	}
}

// assertDeployDurationExemplarsPresent scrapes the /metrics handler
// with the OpenMetrics Accept header and asserts the
// registry_deploy_duration_seconds histogram carries trace_id
// exemplars. Without exemplars Grafana panels cannot drill from a
// slow-bucket bar to the Jaeger waterfall that produced it.
func assertDeployDurationExemplarsPresent(t *testing.T, reg registryHandle) {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "application/openmetrics-text")
	reg.metrics.Handler().ServeHTTP(rr, req)
	body, _ := io.ReadAll(rr.Body)
	out := string(body)
	// Exemplar line shape: `bucket_le ... # {trace_id="..."} value timestamp`.
	if !strings.Contains(out, `# {trace_id="`) {
		t.Errorf("registry_deploy_duration_seconds carries no trace_id exemplar in OpenMetrics exposition\n%s", out)
	}
}

type registryHandle struct {
	URL     string
	server  *httptest.Server
	spans   *tracetest.SpanRecorder
	metrics *prom.HTTPMetrics
	audit   audit.Reader
}

func (r registryHandle) Close() { r.server.Close() }

// bootRegistry wires the same Deps shape the cmd shell wires, but in
// memory: memstore + memstate + memaudit, static discovery pointing
// at markup-svc, and the real rolling deployer (no stub). httptest
// server exposes the HTTP surface on a free port; the URL is used by
// the test's HTTP client calls.
func bootRegistry(t *testing.T, markupSvcURL string) registryHandle {
	t.Helper()

	// Install a recording TracerProvider so the e2e can assert the
	// lifecycle child spans land. The global is restored on cleanup.
	spanRec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	prevTP := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prevTP) })

	disc, err := static.NewFromMap(map[string][]string{
		"production": {markupSvcURL},
	})
	if err != nil {
		t.Fatalf("static.NewFromMap: %v", err)
	}

	st := memstore.New()
	envState := memstate.New()
	au := memaudit.New()
	idgen := ulid.New()
	dep := rolling.New()
	sink := &discardSink{}
	metrics := prom.New()
	now := func() time.Time { return time.Now().UTC() }

	uploadDeps := httpapi.UploadDeps{
		Substrate: st,
		Audit:     au,
		ULID:      idgen,
		Logger:    sink,
		Now:       now,
		Metrics:   metrics,
	}
	promoteDeps := &httpapi.PromoteDeps{
		Artifacts: st,
		EnvState:  envState,
		Audit:     au,
		Discovery: disc,
		Deployer:  dep,
		ULID:      idgen,
		Logger:    sink,
		Now:       now,
		Metrics:   metrics,
	}
	rollbackDeps := &httpapi.RollbackDeps{
		Artifacts: st,
		EnvState:  envState,
		Audit:     au,
		Discovery: disc,
		Deployer:  dep,
		ULID:      idgen,
		Logger:    sink,
		Now:       now,
		Metrics:   metrics,
	}

	deps := httpapi.Deps{
		AccessLog: sink,
		Metrics:   noopMetrics{},
		PanicSink: sink,
		Tracer:    sdktrace.NewTracerProvider().Tracer("e2e"),
		Ready:     func() (string, bool) { return "", true },
		Artifacts: st,
		EnvState:  envState,
		Audit:     au,
		Upload:    &uploadDeps,
		Promote:   promoteDeps,
		Rollback:  rollbackDeps,
	}
	srv := httptest.NewServer(httpapi.NewRouter(deps, metrics.Handler()))
	return registryHandle{
		URL:     srv.URL,
		server:  srv,
		spans:   spanRec,
		metrics: metrics,
		audit:   au,
	}
}

func uploadCSV(t *testing.T, registryURL string, csv []byte) string {
	t.Helper()
	body, ct := buildMultipartCSV(t, csv)
	resp := mustPOST(t, registryURL+"/upload", ct, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("/upload: status=%d body=%s", resp.StatusCode, raw)
	}
	var r httpapi.UploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode /upload: %v", err)
	}
	if r.Hash == "" {
		t.Fatalf("/upload returned empty hash: %+v", r)
	}
	return r.Hash
}

func promote(t *testing.T, registryURL, hash string) {
	t.Helper()
	body, _ := json.Marshal(httpapi.PromoteRequest{
		Hash: hash, Env: "production", Role: "champion", Operator: "e2e-bot", Reason: "lifecycle E2E",
	})
	resp := mustPOST(t, registryURL+"/promote", "application/json", bytes.NewReader(body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("/promote: status=%d body=%s", resp.StatusCode, raw)
	}
}

func rollback(t *testing.T, registryURL string) {
	t.Helper()
	body, _ := json.Marshal(httpapi.RollbackRequest{
		Env: "production", Operator: "e2e-bot", Reason: "B misbehaved",
	})
	resp := mustPOST(t, registryURL+"/rollback", "application/json", bytes.NewReader(body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("/rollback: status=%d body=%s", resp.StatusCode, raw)
	}
}

func listAudit(t *testing.T, registryURL string) audit.Page {
	t.Helper()
	resp, err := http.Get(registryURL + "/audit?limit=100")
	if err != nil {
		t.Fatalf("/audit: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("/audit: status=%d body=%s", resp.StatusCode, raw)
	}
	var p audit.Page
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatalf("decode /audit: %v", err)
	}
	return p
}

func decideRule(t *testing.T, svc string) string {
	t.Helper()
	d := decideEnterprise(t, svc)
	return d.Rule
}

func markupSvcURL() string {
	if v := os.Getenv("MARKUP_SVC_URL"); v != "" {
		return v
	}
	return "http://localhost:8080"
}

func buildMultipartCSV(t *testing.T, csv []byte) (io.Reader, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="source"; filename="rules.csv"`)
	hdr.Set("Content-Type", "text/csv")
	part, err := w.CreatePart(hdr)
	if err != nil {
		t.Fatalf("create source part: %v", err)
	}
	if _, err := part.Write(csv); err != nil {
		t.Fatalf("write source part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	return body, w.FormDataContentType()
}

func mustPOST(t *testing.T, url, ct string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, body)
	if err != nil {
		t.Fatalf("build POST %s: %v", url, err)
	}
	req.Header.Set("Content-Type", ct)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

type discardSink struct{}

func (discardSink) Info(string, map[string]any)  {}
func (discardSink) Error(string, map[string]any) {}

type noopMetrics struct{}

func (noopMetrics) RecordRequest(method, path, status string, duration time.Duration) {}

// --- markup-svc helpers (mirror of scientific/v0.0.1/e2e_body_push_test.go) ---

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

// restoreFromDisk asks markup-svc to reload its on-disk rules, leaving
// it in the state we found it. Best-effort: a failure logs but does
// not fail the test (the test's assertions are the truth).
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
