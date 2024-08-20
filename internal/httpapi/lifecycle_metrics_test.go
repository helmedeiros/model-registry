package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/httpapi"
	"github.com/helmedeiros/model-registry/internal/store"
)

// fakeLifecycleMetrics records every metric call so a single test can
// assert the right outcome label fires for the right code path.
type fakeLifecycleMetrics struct {
	mu             sync.Mutex
	uploads        []string
	promotions     [][3]string
	rollbacks      [][2]string
	deploys        []string
	deployDuration []time.Duration
	stateDrifts    []string
}

func (f *fakeLifecycleMetrics) RecordUpload(outcome string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploads = append(f.uploads, outcome)
}
func (f *fakeLifecycleMetrics) RecordPromotion(env, role, outcome string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.promotions = append(f.promotions, [3]string{env, role, outcome})
}
func (f *fakeLifecycleMetrics) RecordRollback(env, outcome string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rollbacks = append(f.rollbacks, [2]string{env, outcome})
}
func (f *fakeLifecycleMetrics) RecordDeploy(outcome string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deploys = append(f.deploys, outcome)
}
func (f *fakeLifecycleMetrics) ObserveDeployDuration(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deployDuration = append(f.deployDuration, d)
}
func (f *fakeLifecycleMetrics) RecordStateDrift(env string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stateDrifts = append(f.stateDrifts, env)
}

// TestLifecycleMetricsTickThroughUploadPromote drives /upload (200)
// then /promote (200 ok) and asserts the metrics interface saw each
// outcome exactly once at the right labels. Catches a future call-site
// regression where a new outcome branch forgets to call Metrics.
func TestLifecycleMetricsTickThroughUploadPromote(t *testing.T) {
	fake := &fakeLifecycleMetrics{}

	uploadDeps, st, _, _ := newUploadDeps(t)
	uploadDeps.Metrics = fake
	body, ct := multipartBody(t, map[string]uploadPart{
		"source": {filename: "rules.csv", contentType: "text/csv", body: []byte("alpha,rule,1.0,1\n")},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", ct)
	httpapi.Upload(uploadDeps).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/upload: %d %s", rec.Code, rec.Body.String())
	}
	var ur httpapi.UploadResponse
	if err := json.NewDecoder(rec.Body).Decode(&ur); err != nil {
		t.Fatal(err)
	}

	promoteDeps, _, _, _, _ := newPromoteDeps(t, okResult("http://markup-svc-1:8080"))
	promoteDeps.Artifacts = st
	promoteDeps.Metrics = fake
	rec = httptest.NewRecorder()
	pbody, _ := json.Marshal(httpapi.PromoteRequest{
		Hash: ur.Hash, Env: "production", Role: "champion", Operator: "alice", Reason: "metrics-test",
	})
	req = httptest.NewRequest(http.MethodPost, "/promote", bytes.NewReader(pbody))
	httpapi.Promote(promoteDeps).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/promote: %d %s", rec.Code, rec.Body.String())
	}

	if got := fake.uploads; len(got) != 1 || got[0] != "ok" {
		t.Fatalf("uploads counter: %v want [ok]", got)
	}
	if got := fake.promotions; len(got) != 1 || got[0] != [3]string{"production", "champion", "ok"} {
		t.Fatalf("promotions counter: %v want [production champion ok]", got)
	}
	if got := fake.deploys; len(got) != 1 || got[0] != "deployed" {
		t.Fatalf("deploys counter: %v want [deployed]", got)
	}
	if got := fake.deployDuration; len(got) != 1 {
		t.Fatalf("deploy duration observed=%d want 1", len(got))
	}
}

// TestLifecycleMetricsTickRollback drives an upload+promote+rollback
// sequence and asserts the rollback's "ok" outcome lands. Validates
// the rollback handler's metrics call sites end-to-end.
func TestLifecycleMetricsTickRollback(t *testing.T) {
	fake := &fakeLifecycleMetrics{}

	// Seed two champions so rollback has a prior to fall back to.
	rbDeps, st, envState, _, _ := newRollbackDeps(t, okResult("http://markup-svc-1:8080"))
	rbDeps.Metrics = fake
	h1 := putRule(t, st, []byte("alpha,rule,1.0,1\n"))
	h2 := putRule(t, st, []byte("beta,rule,1.0,1\n"))
	for _, h := range []string{string(h1), string(h2)} {
		if _, err := envState.PromoteChampion(context.Background(), "production", store.Hash(h), "ci-bot", "seed"); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	rbBody, _ := json.Marshal(httpapi.RollbackRequest{
		Env: "production", Operator: "alice", Reason: "metrics-test",
	})
	req := httptest.NewRequest(http.MethodPost, "/rollback", bytes.NewReader(rbBody))
	httpapi.Rollback(rbDeps).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/rollback: %d %s", rec.Code, rec.Body.String())
	}

	if got := fake.rollbacks; len(got) != 1 || got[0] != [2]string{"production", "ok"} {
		t.Fatalf("rollbacks counter: %v want [production ok]", got)
	}
	if got := fake.deploys; len(got) != 1 || got[0] != "deployed" {
		t.Fatalf("deploys counter: %v want [deployed]", got)
	}
	if got := fake.deployDuration; len(got) != 1 {
		t.Fatalf("deploy duration observed=%d want 1", len(got))
	}
}
