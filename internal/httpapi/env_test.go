package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/envstate"
	"github.com/helmedeiros/model-registry/internal/envstate/memstate"
	"github.com/helmedeiros/model-registry/internal/httpapi"
)

func TestEnvStateUnknownEnvReturnsEmptyEnvelope(t *testing.T) {
	rec, req := newEnvRequest("production", "/env/production/state")
	httpapi.EnvState(memstate.New()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	got := decodeEnvState(t, rec.Body)
	if got.Env != "production" {
		t.Fatalf("env=%q want production", got.Env)
	}
	if got.Champion != nil || got.Challenger != nil {
		t.Fatalf("unknown env should be empty; got %+v", got)
	}
}

func TestEnvStateMissingEnvReturns400(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/env//state", nil)
	httpapi.EnvState(memstate.New()).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestEnvStateRejectsWrongMethod(t *testing.T) {
	rec, req := newEnvRequest("production", "/env/production/state")
	req.Method = http.MethodPost
	httpapi.EnvState(memstate.New()).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}

func TestEnvStatePanicsAtConstructionOnNilReader(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	httpapi.EnvState(nil)
}

func TestEnvHistoryUnknownEnvReturnsEmptyPage(t *testing.T) {
	rec, req := newEnvRequest("production", "/env/production/history")
	httpapi.EnvHistory(memstate.New()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	page := decodeEnvHistoryPage(t, rec.Body)
	if len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatalf("unknown env history should be empty: %+v", page)
	}
}

func TestEnvHistoryRejectsInvalidLimit(t *testing.T) {
	rec, req := newEnvRequest("production", "/env/production/history?limit=many")
	httpapi.EnvHistory(memstate.New()).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "invalid_limit" {
		t.Fatalf("reason=%q want invalid_limit", got)
	}
}

func TestEnvHistoryRejectsOverLimit(t *testing.T) {
	rec, req := newEnvRequest("production", "/env/production/history?limit=5000")
	httpapi.EnvHistory(memstate.New()).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	if got := bodyReason(t, rec.Body); got != "limit_too_large" {
		t.Fatalf("reason=%q want limit_too_large", got)
	}
}

func TestEnvHistoryMissingEnvReturns400(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/env//history", nil)
	httpapi.EnvHistory(memstate.New()).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestEnvHistoryRejectsWrongMethod(t *testing.T) {
	rec, req := newEnvRequest("production", "/env/production/history")
	req.Method = http.MethodPut
	httpapi.EnvHistory(memstate.New()).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}

func TestEnvHistoryPanicsAtConstructionOnNilReader(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	httpapi.EnvHistory(nil)
}

// Wire-format coverage: stub envstate.Reader so the test pins the
// JSON shape independently of the memstate backing's storage details.
type stubEnvReader struct {
	state   envstate.State
	history envstate.HistoryPage
}

func (s stubEnvReader) Get(_ context.Context, _ string) (envstate.State, error) {
	return s.state, nil
}

func (s stubEnvReader) History(_ context.Context, _ string, _ envstate.ListOptions) (envstate.HistoryPage, error) {
	return s.history, nil
}

func TestEnvStateAndHistorySerializePopulatedEntries(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	reader := stubEnvReader{
		state: envstate.State{
			Env:       "production",
			Champion:  &envstate.Role{Hash: "abc", PromotedBy: "alice", PromotedAt: at},
			UpdatedAt: at,
		},
		history: envstate.HistoryPage{
			Items: []envstate.Transition{
				{Env: "production", Kind: envstate.KindChampionPromoted, ToHash: "abc", Operator: "alice", Reason: "weekly canary", At: at},
			},
		},
	}

	rec, req := newEnvRequest("production", "/env/production/state")
	httpapi.EnvState(reader).ServeHTTP(rec, req)
	got := decodeEnvState(t, rec.Body)
	if got.Champion == nil || got.Champion.Hash != "abc" || got.Champion.PromotedBy != "alice" {
		t.Fatalf("state: %+v", got)
	}
	if got.UpdatedAt == nil || *got.UpdatedAt == "" {
		t.Fatal("updated_at must be populated when state is seeded")
	}

	rec, req = newEnvRequest("production", "/env/production/history")
	httpapi.EnvHistory(reader).ServeHTTP(rec, req)
	page := decodeEnvHistoryPage(t, rec.Body)
	if len(page.Items) != 1 || page.Items[0].ToHash != "abc" || page.Items[0].Kind != string(envstate.KindChampionPromoted) {
		t.Fatalf("history: %+v", page)
	}
}

func newEnvRequest(env, path string) (*httptest.ResponseRecorder, *http.Request) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.SetPathValue("env", env)
	return rec, req
}

func decodeEnvState(t *testing.T, r io.Reader) httpapi.EnvStateView {
	t.Helper()
	var v httpapi.EnvStateView
	if err := json.NewDecoder(r).Decode(&v); err != nil {
		t.Fatalf("decode env state: %v", err)
	}
	return v
}

func decodeEnvHistoryPage(t *testing.T, r io.Reader) httpapi.EnvHistoryPage {
	t.Helper()
	var p httpapi.EnvHistoryPage
	if err := json.NewDecoder(r).Decode(&p); err != nil {
		t.Fatalf("decode env history: %v", err)
	}
	return p
}
