package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/audit/memaudit"
	"github.com/helmedeiros/model-registry/internal/deployer"
	"github.com/helmedeiros/model-registry/internal/envstate"
	"github.com/helmedeiros/model-registry/internal/envstate/memstate"
	"github.com/helmedeiros/model-registry/internal/httpapi"
	"github.com/helmedeiros/model-registry/internal/instances"
	"github.com/helmedeiros/model-registry/internal/store"
	"github.com/helmedeiros/model-registry/internal/store/memstore"
)

type stubDiscovery struct {
	targets []instances.Instance
	err     error
}

func (s stubDiscovery) Instances(_ context.Context, _ string) ([]instances.Instance, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.targets, nil
}

type stubDeployer struct {
	out deployer.DeployResult
	err error
}

func (s stubDeployer) Deploy(_ context.Context, _ []instances.Instance, _ deployer.Body) (deployer.DeployResult, error) {
	return s.out, s.err
}

func okResult(urls ...string) deployer.DeployResult {
	out := deployer.DeployResult{Outcome: deployer.OutcomeOK}
	for _, u := range urls {
		out.Instances = append(out.Instances, deployer.InstanceResult{URL: u, Status: deployer.StatusDeployed, Duration: 5 * time.Millisecond})
	}
	return out
}

func partialResult() deployer.DeployResult {
	return deployer.DeployResult{
		Outcome: deployer.OutcomePartial,
		Instances: []deployer.InstanceResult{
			{URL: "http://good", Status: deployer.StatusDeployed, Duration: 5 * time.Millisecond},
			{URL: "http://bad", Status: deployer.StatusFailed, Duration: 5 * time.Millisecond, Error: "synthetic"},
		},
	}
}

func failedResult() deployer.DeployResult {
	return deployer.DeployResult{
		Outcome: deployer.OutcomeFailed,
		Instances: []deployer.InstanceResult{
			{URL: "http://bad", Status: deployer.StatusFailed, Duration: 5 * time.Millisecond, Error: "synthetic"},
		},
	}
}

func newPromoteDeps(t testing.TB, deploy deployer.DeployResult) (httpapi.PromoteDeps, store.Store, envstate.Store, audit.Reader, *captureSink) {
	t.Helper()
	st := memstore.New()
	envState := memstate.New()
	au := memaudit.New()
	sink := &captureSink{}
	return httpapi.PromoteDeps{
		Artifacts: st,
		EnvState:  envState,
		Audit:     au,
		Discovery: stubDiscovery{targets: []instances.Instance{{URL: "http://markup-svc-1:8080", Env: "production"}}},
		Deployer:  stubDeployer{out: deploy},
		ULID:      &stubULID{},
		Logger:    sink,
		Now:       func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}, st, envState, au, sink
}

func putRule(t testing.TB, s store.Store, src []byte) store.Hash {
	t.Helper()
	h, err := s.Put(context.Background(), store.PutRequest{
		SourceBytes: src,
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedBy: "ci-bot"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func promoteBody(t testing.TB, r httpapi.PromoteRequest) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(r); err != nil {
		t.Fatal(err)
	}
	return buf
}

func TestPromoteHappyPathCommitsStateAndRecordsAudit(t *testing.T) {
	deps, st, envState, au, _ := newPromoteDeps(t, okResult("http://markup-svc-1:8080"))
	h := putRule(t, st, []byte("alpha,rule,1.0,1\n"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/promote", promoteBody(t, httpapi.PromoteRequest{
		Hash: string(h), Env: "production", Role: "champion", Operator: "alice", Reason: "weekly",
	}))
	httpapi.Promote(deps).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp httpapi.PromoteResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.NewHash != string(h) || resp.Env != "production" || resp.Deploy.Outcome != "ok" {
		t.Fatalf("response: %+v", resp)
	}

	state, _ := envState.Get(context.Background(), "production")
	if state.Champion == nil || state.Champion.Hash != h {
		t.Fatalf("envstate champion not set: %+v", state)
	}
	page, _ := au.List(context.Background(), audit.ListOptions{})
	if len(page.Items) != 1 || page.Items[0].Action != "promote" {
		t.Fatalf("audit not recorded: %+v", page)
	}
}

func TestPromotePartialDeployCommitsStateAndStampsHeader(t *testing.T) {
	deps, st, envState, _, _ := newPromoteDeps(t, partialResult())
	h := putRule(t, st, []byte("alpha,rule,1.0,1\n"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/promote", promoteBody(t, httpapi.PromoteRequest{
		Hash: string(h), Env: "production", Role: "champion", Operator: "alice",
	}))
	httpapi.Promote(deps).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (partial commits state)", rec.Code)
	}
	if rec.Header().Get("X-Partial-Deploy") != "true" {
		t.Fatal("X-Partial-Deploy header should signal partial outcome")
	}
	state, _ := envState.Get(context.Background(), "production")
	if state.Champion == nil || state.Champion.Hash != h {
		t.Fatalf("partial-deploy should commit state per ADR-0005: %+v", state)
	}
}

func TestPromoteFailedDeployLeavesStateUntouchedAndReturns502(t *testing.T) {
	deps, st, envState, au, _ := newPromoteDeps(t, failedResult())
	h := putRule(t, st, []byte("alpha,rule,1.0,1\n"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/promote", promoteBody(t, httpapi.PromoteRequest{
		Hash: string(h), Env: "production", Role: "champion", Operator: "alice",
	}))
	httpapi.Promote(deps).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d want 502 on OutcomeFailed", rec.Code)
	}
	state, _ := envState.Get(context.Background(), "production")
	if state.Champion != nil {
		t.Fatalf("OutcomeFailed must NOT commit state: %+v", state)
	}
	page, _ := au.List(context.Background(), audit.ListOptions{})
	if len(page.Items) != 0 {
		t.Fatalf("OutcomeFailed should not append a promote audit: %+v", page)
	}
}

func TestPromoteUnknownHashReturns400HashUnknown(t *testing.T) {
	deps, _, _, _, _ := newPromoteDeps(t, okResult("http://markup-svc-1:8080"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/promote", promoteBody(t, httpapi.PromoteRequest{
		Hash: "missing", Env: "production", Role: "champion", Operator: "alice",
	}))
	httpapi.Promote(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "hash_unknown" {
		t.Fatalf("reason=%q want hash_unknown", got)
	}
}

func TestPromoteDeprecatedHashRejectedAt400(t *testing.T) {
	deps, st, _, _, _ := newPromoteDeps(t, okResult("http://markup-svc-1:8080"))
	h := putRule(t, st, []byte("alpha,rule,1.0,1\n"))
	if err := st.Deprecate(context.Background(), h, "obsolete"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/promote", promoteBody(t, httpapi.PromoteRequest{
		Hash: string(h), Env: "production", Role: "champion", Operator: "alice",
	}))
	httpapi.Promote(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "hash_deprecated" {
		t.Fatalf("reason=%q want hash_deprecated", got)
	}
}

func TestPromoteNoInstancesReturns400InvalidEnv(t *testing.T) {
	deps, st, _, _, _ := newPromoteDeps(t, okResult())
	deps.Discovery = stubDiscovery{err: instances.ErrNoInstances}
	h := putRule(t, st, []byte("alpha,rule,1.0,1\n"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/promote", promoteBody(t, httpapi.PromoteRequest{
		Hash: string(h), Env: "staging", Role: "champion", Operator: "alice",
	}))
	httpapi.Promote(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "invalid_env" {
		t.Fatalf("reason=%q want invalid_env", got)
	}
}

func TestPromoteChallengerRoleReturns501(t *testing.T) {
	deps, st, _, _, _ := newPromoteDeps(t, okResult("http://markup-svc-1:8080"))
	h := putRule(t, st, []byte("alpha,rule,1.0,1\n"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/promote", promoteBody(t, httpapi.PromoteRequest{
		Hash: string(h), Env: "production", Role: "challenger", Operator: "alice",
	}))
	httpapi.Promote(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status=%d want 501", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "challenger_not_implemented" {
		t.Fatalf("reason=%q want challenger_not_implemented", got)
	}
}

func TestPromoteValidationRejectsMissingFields(t *testing.T) {
	deps, _, _, _, _ := newPromoteDeps(t, okResult())
	for _, tc := range []struct {
		name string
		req  httpapi.PromoteRequest
		want string
	}{
		{"missing hash", httpapi.PromoteRequest{Env: "p", Role: "champion", Operator: "a"}, "invalid_hash"},
		{"missing env", httpapi.PromoteRequest{Hash: "h", Role: "champion", Operator: "a"}, "invalid_env"},
		{"missing role", httpapi.PromoteRequest{Hash: "h", Env: "p", Operator: "a"}, "invalid_role"},
		{"missing operator", httpapi.PromoteRequest{Hash: "h", Env: "p", Role: "champion"}, "invalid_operator"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/promote", promoteBody(t, tc.req))
		httpapi.Promote(deps).ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d want 400", tc.name, rec.Code)
		}
		if got := bodyReason(t, rec.Body); got != tc.want {
			t.Fatalf("%s: reason=%q want %q", tc.name, got, tc.want)
		}
	}
}

func TestPromoteRejectsInvalidJSON(t *testing.T) {
	deps, _, _, _, _ := newPromoteDeps(t, okResult())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/promote", strings.NewReader("not json"))
	httpapi.Promote(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "invalid_body" {
		t.Fatalf("reason=%q want invalid_body", got)
	}
}

func TestPromoteRejectsWrongMethod(t *testing.T) {
	deps, _, _, _, _ := newPromoteDeps(t, okResult())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/promote", nil)
	httpapi.Promote(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}

func TestPromoteAuditFailureLogsButReturns200(t *testing.T) {
	deps, st, envState, _, sink := newPromoteDeps(t, okResult("http://markup-svc-1:8080"))
	deps.Audit = failingAuditWriter{}
	h := putRule(t, st, []byte("alpha,rule,1.0,1\n"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/promote", promoteBody(t, httpapi.PromoteRequest{
		Hash: string(h), Env: "production", Role: "champion", Operator: "alice",
	}))
	httpapi.Promote(deps).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 even with audit failure", rec.Code)
	}
	state, _ := envState.Get(context.Background(), "production")
	if state.Champion == nil {
		t.Fatal("envstate must still commit when audit fails")
	}
	if sink.msg != "registry.audit.write_failed" {
		t.Fatalf("logger did not see audit-failure event: %+v", sink)
	}
}

func TestPromotePanicsOnMissingDeps(t *testing.T) {
	full := httpapi.PromoteDeps{
		Artifacts: memstore.New(),
		EnvState:  memstate.New(),
		Audit:     memaudit.New(),
		Discovery: stubDiscovery{},
		Deployer:  stubDeployer{},
		ULID:      &stubULID{},
		Logger:    &captureSink{},
	}
	for _, name := range []string{"Artifacts", "EnvState", "Audit", "Discovery", "Deployer", "ULID", "Logger"} {
		t.Run("missing "+name, func(t *testing.T) {
			d := full
			switch name {
			case "Artifacts":
				d.Artifacts = nil
			case "EnvState":
				d.EnvState = nil
			case "Audit":
				d.Audit = nil
			case "Discovery":
				d.Discovery = nil
			case "Deployer":
				d.Deployer = nil
			case "ULID":
				d.ULID = nil
			case "Logger":
				d.Logger = nil
			}
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic")
				}
			}()
			httpapi.Promote(d)
		})
	}
}

var _ = errors.New // keep errors import live when only used in stubs
