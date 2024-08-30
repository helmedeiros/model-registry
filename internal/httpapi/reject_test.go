package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/audit"
	"github.com/helmedeiros/model-registry/internal/audit/memaudit"
	"github.com/helmedeiros/model-registry/internal/envstate/memstate"
	"github.com/helmedeiros/model-registry/internal/httpapi"
	"github.com/helmedeiros/model-registry/internal/instances"
)

func newRejectDeps(t *testing.T) (httpapi.RejectDeps, *memstate.Store, audit.Reader) {
	t.Helper()
	envState := memstate.New()
	au := memaudit.New()
	return httpapi.RejectDeps{
		EnvState: envState,
		Audit:    au,
		ULID:     &stubULID{},
		Logger:   &captureSink{},
		Now:      func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}, envState, au
}

func TestRejectHappyPathClearsStateAndRecordsAudit(t *testing.T) {
	deps, envState, au := newRejectDeps(t)
	if err := envState.PromoteChallenger(context.Background(), "production", "h1", "alice", "shadow"); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(httpapi.RejectRequest{Env: "production", Operator: "alice", Reason: "divergence"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/reject", bytes.NewReader(body))
	httpapi.Reject(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp httpapi.RejectResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.RejectedHash != "h1" {
		t.Fatalf("rejected_hash=%q want h1", resp.RejectedHash)
	}
	state, _ := envState.Get(context.Background(), "production")
	if state.Challenger != nil {
		t.Fatalf("challenger not cleared: %+v", state.Challenger)
	}
	page, _ := au.List(context.Background(), audit.ListOptions{})
	if len(page.Items) != 1 || page.Items[0].Action != "reject_challenger" {
		t.Fatalf("audit: %+v", page.Items)
	}
}

func TestRejectNoChallengerReturns400(t *testing.T) {
	deps, _, _ := newRejectDeps(t)
	body, _ := json.Marshal(httpapi.RejectRequest{Env: "production", Operator: "alice", Reason: "no-op"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/reject", bytes.NewReader(body))
	httpapi.Reject(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	if reason := bodyReason(t, rec.Body); reason != "no_challenger" {
		t.Fatalf("reason=%q want no_challenger", reason)
	}
}

func TestRejectFansOutClearChallengerWhenDeployerWired(t *testing.T) {
	deps, envState, _ := newRejectDeps(t)
	if err := envState.PromoteChallenger(context.Background(), "production", "h1", "alice", "shadow"); err != nil {
		t.Fatal(err)
	}
	deps.Discovery = stubDiscovery{targets: []instances.Instance{{URL: "http://markup-svc-1:8080", Env: "production"}}}
	deps.Deployer = stubDeployer{clearOut: okResult("http://markup-svc-1:8080")}

	body, _ := json.Marshal(httpapi.RejectRequest{Env: "production", Operator: "alice", Reason: "divergence"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/reject", bytes.NewReader(body))
	httpapi.Reject(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp httpapi.RejectResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Deploy.Outcome != "ok" || len(resp.Deploy.Instances) != 1 {
		t.Fatalf("clear-challenger deploy block missing: %+v", resp.Deploy)
	}
}

func TestRejectMissingReasonReturns400(t *testing.T) {
	deps, _, _ := newRejectDeps(t)
	body, _ := json.Marshal(httpapi.RejectRequest{Env: "production", Operator: "alice"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/reject", bytes.NewReader(body))
	httpapi.Reject(deps).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	if reason := bodyReason(t, rec.Body); reason != "reason_required" {
		t.Fatalf("reason=%q want reason_required", reason)
	}
}
