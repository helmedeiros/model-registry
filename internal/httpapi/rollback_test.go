package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
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

func newRollbackDeps(t *testing.T, deploy deployer.DeployResult) (httpapi.RollbackDeps, store.Store, envstate.Store, audit.Reader, *captureSink) {
	t.Helper()
	st := memstore.New()
	envState := memstate.New()
	au := memaudit.New()
	sink := &captureSink{}
	return httpapi.RollbackDeps{
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

func rollbackBody(t *testing.T, r httpapi.RollbackRequest) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(r); err != nil {
		t.Fatal(err)
	}
	return buf
}

func TestRollbackHappyPathRestoresPreviousAndDeploys(t *testing.T) {
	deps, st, envState, au, _ := newRollbackDeps(t, okResult("http://markup-svc-1:8080"))
	h1 := putRule(t, st, []byte("alpha,rule,1.0,1\n"))
	h2 := putRule(t, st, []byte("beta,rule,1.0,1\n"))
	if _, err := envState.PromoteChampion(context.Background(), "production", h1, "ci-bot", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := envState.PromoteChampion(context.Background(), "production", h2, "ci-bot", ""); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/rollback", rollbackBody(t, httpapi.RollbackRequest{
		Env: "production", Operator: "alice", Reason: "h2 misbehaved",
	}))
	httpapi.Rollback(deps).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp httpapi.RollbackResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.PreviousHash != string(h2) || resp.RolledTo != string(h1) {
		t.Fatalf("response: %+v", resp)
	}
	state, _ := envState.Get(context.Background(), "production")
	if state.Champion == nil || state.Champion.Hash != h1 {
		t.Fatalf("champion not restored to h1: %+v", state)
	}
	page, _ := au.List(context.Background(), audit.ListOptions{})
	if len(page.Items) != 1 || page.Items[0].Action != "rollback" {
		t.Fatalf("audit not recorded: %+v", page)
	}
}

func TestRollbackNoHistoryReturns400(t *testing.T) {
	deps, _, _, _, _ := newRollbackDeps(t, okResult())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/rollback", rollbackBody(t, httpapi.RollbackRequest{
		Env: "production", Operator: "alice", Reason: "rollback",
	}))
	httpapi.Rollback(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "no_history" {
		t.Fatalf("reason=%q want no_history", got)
	}
}

func TestRollbackFailedDeployLeavesStateUntouchedAndReturns502(t *testing.T) {
	deps, st, envState, au, _ := newRollbackDeps(t, failedResult())
	h1 := putRule(t, st, []byte("alpha,rule,1.0,1\n"))
	h2 := putRule(t, st, []byte("beta,rule,1.0,1\n"))
	if _, err := envState.PromoteChampion(context.Background(), "production", h1, "ci-bot", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := envState.PromoteChampion(context.Background(), "production", h2, "ci-bot", ""); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/rollback", rollbackBody(t, httpapi.RollbackRequest{
		Env: "production", Operator: "alice", Reason: "h2 misbehaved",
	}))
	httpapi.Rollback(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d want 502", rec.Code)
	}
	state, _ := envState.Get(context.Background(), "production")
	if state.Champion == nil || state.Champion.Hash != h2 {
		t.Fatalf("OutcomeFailed must NOT commit rollback: %+v", state)
	}
	page, _ := au.List(context.Background(), audit.ListOptions{})
	if len(page.Items) != 0 {
		t.Fatalf("OutcomeFailed should not record rollback audit: %+v", page)
	}
}

func TestRollbackPartialDeployCommitsRollbackAndStampsHeader(t *testing.T) {
	deps, st, envState, _, _ := newRollbackDeps(t, partialResult())
	h1 := putRule(t, st, []byte("alpha,rule,1.0,1\n"))
	h2 := putRule(t, st, []byte("beta,rule,1.0,1\n"))
	if _, err := envState.PromoteChampion(context.Background(), "production", h1, "ci-bot", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := envState.PromoteChampion(context.Background(), "production", h2, "ci-bot", ""); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/rollback", rollbackBody(t, httpapi.RollbackRequest{
		Env: "production", Operator: "alice", Reason: "h2 misbehaved",
	}))
	httpapi.Rollback(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (partial commits rollback)", rec.Code)
	}
	if rec.Header().Get("X-Partial-Deploy") != "true" {
		t.Fatal("X-Partial-Deploy header should signal partial outcome")
	}
	state, _ := envState.Get(context.Background(), "production")
	if state.Champion == nil || state.Champion.Hash != h1 {
		t.Fatalf("partial rollback should commit: %+v", state)
	}
}

func TestRollbackMissingReasonReturns400(t *testing.T) {
	deps, _, _, _, _ := newRollbackDeps(t, okResult())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/rollback", rollbackBody(t, httpapi.RollbackRequest{
		Env: "production", Operator: "alice",
	}))
	httpapi.Rollback(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "reason_required" {
		t.Fatalf("reason=%q want reason_required", got)
	}
}

func TestRollbackValidationRejectsMissingFields(t *testing.T) {
	deps, _, _, _, _ := newRollbackDeps(t, okResult())
	for _, tc := range []struct {
		name string
		req  httpapi.RollbackRequest
		want string
	}{
		{"missing env", httpapi.RollbackRequest{Operator: "a", Reason: "r"}, "invalid_env"},
		{"missing operator", httpapi.RollbackRequest{Env: "p", Reason: "r"}, "invalid_operator"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/rollback", rollbackBody(t, tc.req))
		httpapi.Rollback(deps).ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d want 400", tc.name, rec.Code)
		}
		if got := bodyReason(t, rec.Body); got != tc.want {
			t.Fatalf("%s: reason=%q want %q", tc.name, got, tc.want)
		}
	}
}

func TestRollbackRejectsInvalidJSON(t *testing.T) {
	deps, _, _, _, _ := newRollbackDeps(t, okResult())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/rollback", strings.NewReader("not json"))
	httpapi.Rollback(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestRollbackRejectsWrongMethod(t *testing.T) {
	deps, _, _, _, _ := newRollbackDeps(t, okResult())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/rollback", nil)
	httpapi.Rollback(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}

func TestRollbackAuditFailureLogsButReturns200(t *testing.T) {
	deps, st, envState, _, sink := newRollbackDeps(t, okResult("http://markup-svc-1:8080"))
	deps.Audit = failingAuditWriter{}
	h1 := putRule(t, st, []byte("alpha,rule,1.0,1\n"))
	h2 := putRule(t, st, []byte("beta,rule,1.0,1\n"))
	if _, err := envState.PromoteChampion(context.Background(), "production", h1, "ci-bot", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := envState.PromoteChampion(context.Background(), "production", h2, "ci-bot", ""); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/rollback", rollbackBody(t, httpapi.RollbackRequest{
		Env: "production", Operator: "alice", Reason: "h2 misbehaved",
	}))
	httpapi.Rollback(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 even with audit failure", rec.Code)
	}
	if sink.msg != "registry.audit.write_failed" {
		t.Fatalf("logger did not see audit-failure event: %+v", sink)
	}
}

func TestRollbackPanicsOnMissingDeps(t *testing.T) {
	full := httpapi.RollbackDeps{
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
			httpapi.Rollback(d)
		})
	}
}
