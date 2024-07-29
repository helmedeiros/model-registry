// Package envstatetest is the reusable conformance suite every
// envstate backing runs against. A new backing implements its tests
// as `storetest.RunConformance(t, factory)`-style hook (here as
// `RunConformance`) and inherits the full Reader contract coverage.
package envstatetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/envstate"
	"github.com/helmedeiros/model-registry/internal/store"
)

// SeedFunc populates the store directly with fixture state/history,
// bypassing the typed Writer projection (which returns
// ErrNotImplemented in v0.0.3). Every conformance-running backing
// must supply one — Reader-only contract checks depend on a seeded
// fixture and the suite fails loudly when SeedFunc is nil.
type SeedFunc func(state envstate.State, history []envstate.Transition)

// Factory builds a fresh envstate.Store and returns it together with a
// SeedFunc for fixture setup. The *testing.T is the subtest's T.
type Factory func(t *testing.T) (envstate.Store, SeedFunc)

// RunConformance exercises every behaviour envstate.Reader promises
// (Writer cases assert ErrNotImplemented until ADR-0005 lands real
// implementations). Each subtest is independent.
func RunConformance(t *testing.T, factory Factory) {
	t.Helper()
	cases := []struct {
		name string
		fn   func(t *testing.T, factory Factory)
	}{
		{"GetUnknownEnvReturnsEmptyState", testGetUnknownReturnsEmpty},
		{"GetReturnsSeededState", testGetReturnsSeeded},
		{"GetDeepCopiesRoleSoMutationDoesNotBleed", testGetDeepCopiesRole},
		{"HistoryReturnsEmptyOnUnknownEnv", testHistoryEmptyForUnknown},
		{"HistoryOrdersByAtDescending", testHistoryOrdersByAtDesc},
		{"HistoryPaginatesViaCursor", testHistoryPaginates},
		{"HistoryUnknownCursorRestarts", testHistoryUnknownCursor},
		{"HistoryLimitDefaultsAndCaps", testHistoryLimitClamping},
		{"WriterReturnsErrNotImplemented", testWriterStubbed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.fn(t, factory) })
	}
}

func ctx() context.Context { return context.Background() }

func testGetUnknownReturnsEmpty(t *testing.T, mk Factory) {
	s, _ := mk(t)
	got, err := s.Get(ctx(), "production")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Env != "production" {
		t.Fatalf("Env=%q want production", got.Env)
	}
	if got.Champion != nil || got.Challenger != nil {
		t.Fatalf("unknown env should be empty; got %+v", got)
	}
}

func testGetReturnsSeeded(t *testing.T, mk Factory) {
	s, seed := mk(t)
	if seed == nil {
		t.Fatal("conformance requires SeedFunc; backing must provide one")
	}
	at := time.Unix(1_700_000_000, 0).UTC()
	want := envstate.State{
		Env:        "production",
		Champion:   &envstate.Role{Hash: store.Hash("abc"), PromotedBy: "alice", PromotedAt: at},
		Challenger: nil,
		UpdatedAt:  at,
	}
	seed(want, nil)

	got, err := s.Get(ctx(), "production")
	if err != nil {
		t.Fatal(err)
	}
	if got.Champion == nil || got.Champion.Hash != "abc" {
		t.Fatalf("champion not preserved: %+v", got)
	}
}

func testGetDeepCopiesRole(t *testing.T, mk Factory) {
	s, seed := mk(t)
	if seed == nil {
		t.Fatal("conformance requires SeedFunc; backing must provide one")
	}
	at := time.Unix(1_700_000_000, 0).UTC()
	seed(envstate.State{
		Env:       "production",
		Champion:  &envstate.Role{Hash: store.Hash("abc"), PromotedBy: "alice", PromotedAt: at},
		UpdatedAt: at,
	}, nil)

	first, _ := s.Get(ctx(), "production")
	first.Champion.PromotedBy = "MUTATED"

	second, _ := s.Get(ctx(), "production")
	if second.Champion.PromotedBy != "alice" {
		t.Fatalf("mutation bled through deep-copy: %+v", second.Champion)
	}
}

func testHistoryEmptyForUnknown(t *testing.T, mk Factory) {
	s, _ := mk(t)
	page, err := s.History(ctx(), "production", envstate.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatalf("unknown env history should be empty: %+v", page)
	}
}

func testHistoryOrdersByAtDesc(t *testing.T, mk Factory) {
	s, seed := mk(t)
	if seed == nil {
		t.Fatal("conformance requires SeedFunc; backing must provide one")
	}
	seed(envstate.State{}, []envstate.Transition{
		{Env: "production", Kind: envstate.KindChampionPromoted, ToHash: "a", At: time.Unix(1, 0).UTC()},
		{Env: "production", Kind: envstate.KindChampionPromoted, ToHash: "b", At: time.Unix(2, 0).UTC()},
		{Env: "production", Kind: envstate.KindChampionPromoted, ToHash: "c", At: time.Unix(3, 0).UTC()},
	})

	page, err := s.History(ctx(), "production", envstate.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 || page.Items[0].ToHash != "c" || page.Items[2].ToHash != "a" {
		t.Fatalf("newest-first order broken: %+v", page.Items)
	}
}

func testHistoryPaginates(t *testing.T, mk Factory) {
	s, seed := mk(t)
	if seed == nil {
		t.Fatal("conformance requires SeedFunc; backing must provide one")
	}
	seed(envstate.State{}, []envstate.Transition{
		{Env: "production", Kind: envstate.KindChampionPromoted, ToHash: "a", At: time.Unix(1, 0).UTC()},
		{Env: "production", Kind: envstate.KindChampionPromoted, ToHash: "b", At: time.Unix(2, 0).UTC()},
		{Env: "production", Kind: envstate.KindChampionPromoted, ToHash: "c", At: time.Unix(3, 0).UTC()},
	})

	p1, err := s.History(ctx(), "production", envstate.ListOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Items) != 2 || p1.NextCursor == "" {
		t.Fatalf("first page: items=%d next=%q", len(p1.Items), p1.NextCursor)
	}
	p2, err := s.History(ctx(), "production", envstate.ListOptions{Limit: 2, Cursor: p1.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Items) != 1 || p2.NextCursor != "" {
		t.Fatalf("second page: items=%d next=%q", len(p2.Items), p2.NextCursor)
	}
}

func testHistoryUnknownCursor(t *testing.T, mk Factory) {
	s, seed := mk(t)
	if seed == nil {
		t.Fatal("conformance requires SeedFunc; backing must provide one")
	}
	seed(envstate.State{}, []envstate.Transition{
		{Env: "production", Kind: envstate.KindChampionPromoted, ToHash: "a", At: time.Unix(1, 0).UTC()},
	})
	page, err := s.History(ctx(), "production", envstate.ListOptions{Cursor: "no-such-cursor"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("unknown cursor should restart; got %d items", len(page.Items))
	}
}

func testHistoryLimitClamping(t *testing.T, mk Factory) {
	s, seed := mk(t)
	if seed == nil {
		t.Fatal("conformance requires SeedFunc; backing must provide one")
	}
	seed(envstate.State{}, []envstate.Transition{
		{Env: "production", Kind: envstate.KindChampionPromoted, ToHash: "a", At: time.Unix(1, 0).UTC()},
		{Env: "production", Kind: envstate.KindChampionPromoted, ToHash: "b", At: time.Unix(2, 0).UTC()},
	})

	page, err := s.History(ctx(), "production", envstate.ListOptions{Limit: -5})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("negative limit should default, got %d", len(page.Items))
	}
	page, err = s.History(ctx(), "production", envstate.ListOptions{Limit: 9999})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("oversize limit should cap silently, got %d", len(page.Items))
	}
}

func testWriterStubbed(t *testing.T, mk Factory) {
	s, _ := mk(t)
	for _, call := range []struct {
		name string
		fn   func() error
	}{
		{"PromoteChampion", func() error { return s.PromoteChampion(ctx(), "p", store.Hash("h"), "op", "r") }},
		{"RollbackChampion", func() error { return s.RollbackChampion(ctx(), "p", "op", "r") }},
		{"PromoteChallenger", func() error { return s.PromoteChallenger(ctx(), "p", store.Hash("h"), "op", "r") }},
		{"RejectChallenger", func() error { return s.RejectChallenger(ctx(), "p", "op", "r") }},
	} {
		if err := call.fn(); !errors.Is(err, envstate.ErrNotImplemented) {
			t.Fatalf("%s: err=%v want ErrNotImplemented", call.name, err)
		}
	}
}
