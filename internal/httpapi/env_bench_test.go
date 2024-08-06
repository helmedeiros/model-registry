package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/envstate"
	"github.com/helmedeiros/model-registry/internal/httpapi"
	"github.com/helmedeiros/model-registry/internal/store"
)

type seededEnvReader struct {
	state   envstate.State
	history envstate.HistoryPage
}

func (s seededEnvReader) Get(_ context.Context, _ string) (envstate.State, error) {
	return s.state, nil
}
func (s seededEnvReader) History(_ context.Context, _ string, _ envstate.ListOptions) (envstate.HistoryPage, error) {
	return s.history, nil
}
func (s seededEnvReader) PreviousChampion(_ context.Context, _ string) (store.Hash, error) {
	return "", envstate.ErrNoPreviousChampion
}

// BenchmarkGET_EnvState pins the per-request cost of /env/{env}/state
// against ADR-0004's < 5 ms bar. Memstore-style backing (in-memory
// single struct read); the fsstate-on-NVMe wall-clock cost is
// captured separately when that backing lands.
func BenchmarkGET_EnvState(b *testing.B) {
	at := time.Unix(1_700_000_000, 0).UTC()
	reader := seededEnvReader{state: envstate.State{
		Env:       "production",
		Champion:  &envstate.Role{Hash: store.Hash("abc"), PromotedBy: "alice", PromotedAt: at},
		UpdatedAt: at,
	}}
	handler := httpapi.EnvState(reader)
	req := httptest.NewRequest(http.MethodGet, "/env/production/state", nil)
	req.SetPathValue("env", "production")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}

// BenchmarkGET_EnvHistory_100Entries pins the per-request cost of
// /env/{env}/history against ADR-0004's < 50 ms bar at the
// 100-entry page scale.
func BenchmarkGET_EnvHistory_100Entries(b *testing.B) {
	at := time.Unix(1_700_000_000, 0).UTC()
	items := make([]envstate.Transition, 100)
	for i := range items {
		items[i] = envstate.Transition{
			Env:      "production",
			Kind:     envstate.KindChampionPromoted,
			ToHash:   store.Hash(strconv.Itoa(i)),
			Operator: "alice",
			At:       at.Add(time.Duration(i) * time.Millisecond),
		}
	}
	reader := seededEnvReader{history: envstate.HistoryPage{Items: items}}
	handler := httpapi.EnvHistory(reader)
	req := httptest.NewRequest(http.MethodGet, "/env/production/history?limit=100", nil)
	req.SetPathValue("env", "production")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}
