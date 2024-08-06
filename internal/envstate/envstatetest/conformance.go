// Package envstatetest is the reusable conformance suite every
// envstate backing runs against. A new backing implements its tests
// as `storetest.RunConformance(t, factory)`-style hook (here as
// `RunConformance`) and inherits the full Reader and Writer contract
// coverage.
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
// bypassing the typed Writer projection. The Reader-only cases
// established the contract before the Writer impls existed; the
// Writer cases now rely on the real impls. Every backing must supply
// a SeedFunc — Reader contract checks depend on a seeded
// fixture and the suite fails loudly when SeedFunc is nil.
type SeedFunc func(state envstate.State, history []envstate.Transition)

// Factory builds a fresh envstate.Store and returns it together with a
// SeedFunc for fixture setup. The *testing.T is the subtest's T.
type Factory func(t *testing.T) (envstate.Store, SeedFunc)

// RunConformance exercises every behaviour the envstate Reader and
// Writer projections promise. Challenger Writer cases assert
// ErrNotImplemented until ADR-0006. Each subtest is independent.
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
		{"PromoteChampionFromEmptyEnvSetsState", testPromoteFromEmpty},
		{"PromoteChampionRecordsPreviousHash", testPromoteRecordsPrevious},
		{"PromoteChampionAppendsHistory", testPromoteAppendsHistory},
		{"PromoteChampionValidatesRequiredFields", testPromoteValidation},
		{"RollbackChampionRestoresPreviousHash", testRollbackRestoresPrevious},
		{"RollbackChampionReturnsRolledToHash", testRollbackReturnsRolledTo},
		{"RollbackChampionWithRepeatedHashRestoresPrior", testRollbackRepeatedHash},
		{"PreviousChampionMatchesRollbackResult", testPreviousChampionMatchesRollback},
		{"RollbackChampionWithoutHistoryErrors", testRollbackWithoutHistory},
		{"RollbackChampionWithoutChampionErrors", testRollbackWithoutChampion},
		{"GetReturnsDeepCopiedRoleAfterPromote", testGetDeepCopiesAfterPromote},
		{"ChallengerWritersReturnErrNotImplemented", testChallengerStubbed},
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

func testPromoteFromEmpty(t *testing.T, mk Factory) {
	s, _ := mk(t)
	if _, err := s.PromoteChampion(ctx(), "production", store.Hash("h1"), "alice", "first cut"); err != nil {
		t.Fatalf("PromoteChampion: %v", err)
	}
	got, _ := s.Get(ctx(), "production")
	if got.Champion == nil || got.Champion.Hash != "h1" || got.Champion.PromotedBy != "alice" {
		t.Fatalf("state after promote: %+v", got)
	}
}

func testPromoteRecordsPrevious(t *testing.T, mk Factory) {
	s, _ := mk(t)
	if _, err := s.PromoteChampion(ctx(), "production", store.Hash("h1"), "alice", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromoteChampion(ctx(), "production", store.Hash("h2"), "alice", "weekly"); err != nil {
		t.Fatal(err)
	}
	page, _ := s.History(ctx(), "production", envstate.ListOptions{})
	if len(page.Items) != 2 {
		t.Fatalf("want 2 history entries, got %d", len(page.Items))
	}
	if page.Items[0].FromHash != "h1" || page.Items[0].ToHash != "h2" {
		t.Fatalf("newest entry should record previous->new: %+v", page.Items[0])
	}
	if page.Items[1].FromHash != "" || page.Items[1].ToHash != "h1" {
		t.Fatalf("first entry should have empty FromHash: %+v", page.Items[1])
	}
}

func testPromoteAppendsHistory(t *testing.T, mk Factory) {
	s, _ := mk(t)
	for _, h := range []string{"a", "b", "c"} {
		if _, err := s.PromoteChampion(ctx(), "production", store.Hash(h), "alice", ""); err != nil {
			t.Fatal(err)
		}
	}
	page, _ := s.History(ctx(), "production", envstate.ListOptions{})
	if len(page.Items) != 3 {
		t.Fatalf("want 3 history entries, got %d", len(page.Items))
	}
	for _, kind := range []envstate.Kind{page.Items[0].Kind, page.Items[1].Kind, page.Items[2].Kind} {
		if kind != envstate.KindChampionPromoted {
			t.Fatalf("kind=%s want champion_promoted", kind)
		}
	}
}

func testPromoteValidation(t *testing.T, mk Factory) {
	s, _ := mk(t)
	for _, tc := range []struct {
		name string
		env  string
		hash store.Hash
		op   string
		want error
	}{
		{"empty env", "", "h", "alice", envstate.ErrEnvRequired},
		{"empty hash", "p", "", "alice", envstate.ErrHashRequired},
		{"empty operator", "p", "h", "", envstate.ErrOperatorRequired},
	} {
		if _, err := s.PromoteChampion(ctx(), tc.env, tc.hash, tc.op, ""); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err=%v want %v", tc.name, err, tc.want)
		}
	}
}

func testRollbackRestoresPrevious(t *testing.T, mk Factory) {
	s, _ := mk(t)
	for _, h := range []string{"h1", "h2"} {
		if _, err := s.PromoteChampion(ctx(), "production", store.Hash(h), "alice", ""); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.RollbackChampion(ctx(), "production", "alice", "h2 misbehaved"); err != nil {
		t.Fatalf("RollbackChampion: %v", err)
	}
	got, _ := s.Get(ctx(), "production")
	if got.Champion == nil || got.Champion.Hash != "h1" {
		t.Fatalf("after rollback champion=%+v want hash=h1", got.Champion)
	}
	page, _ := s.History(ctx(), "production", envstate.ListOptions{})
	if page.Items[0].Kind != envstate.KindChampionRolledBack {
		t.Fatalf("newest entry kind=%s want champion_rolled_back", page.Items[0].Kind)
	}
	if page.Items[0].FromHash != "h2" || page.Items[0].ToHash != "h1" {
		t.Fatalf("rollback entry: %+v", page.Items[0])
	}
}

func testRollbackReturnsRolledTo(t *testing.T, mk Factory) {
	s, _ := mk(t)
	if _, err := s.PromoteChampion(ctx(), "production", store.Hash("h1"), "alice", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromoteChampion(ctx(), "production", store.Hash("h2"), "alice", ""); err != nil {
		t.Fatal(err)
	}
	rolled, err := s.RollbackChampion(ctx(), "production", "alice", "")
	if err != nil {
		t.Fatalf("RollbackChampion: %v", err)
	}
	if rolled != store.Hash("h1") {
		t.Fatalf("returned hash=%q want h1", rolled)
	}
}

func testPreviousChampionMatchesRollback(t *testing.T, mk Factory) {
	s, _ := mk(t)
	if _, err := s.PromoteChampion(ctx(), "production", store.Hash("h1"), "alice", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromoteChampion(ctx(), "production", store.Hash("h2"), "alice", ""); err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviousChampion(ctx(), "production")
	if err != nil {
		t.Fatalf("PreviousChampion: %v", err)
	}
	rolled, err := s.RollbackChampion(ctx(), "production", "alice", "")
	if err != nil {
		t.Fatalf("RollbackChampion: %v", err)
	}
	if preview != rolled {
		t.Fatalf("preview=%q rolled=%q — single-threaded race-free path must match", preview, rolled)
	}

	if _, err := s.PreviousChampion(ctx(), "unknown-env"); !errors.Is(err, envstate.ErrNoChampion) {
		t.Fatalf("PreviousChampion(unknown): err=%v want ErrNoChampion", err)
	}
}

// testRollbackRepeatedHash pins the previousChampionHash walk's
// behaviour when a hash repeats: h1 → h2 → h1 then rollback must
// restore h2, not h1 (the current). A naive "latest different ToHash"
// walker would mistake the original h1 entry.
func testRollbackRepeatedHash(t *testing.T, mk Factory) {
	s, _ := mk(t)
	for _, h := range []string{"h1", "h2", "h1"} {
		if _, err := s.PromoteChampion(ctx(), "production", store.Hash(h), "alice", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.RollbackChampion(ctx(), "production", "alice", ""); err != nil {
		t.Fatalf("RollbackChampion: %v", err)
	}
	got, _ := s.Get(ctx(), "production")
	if got.Champion == nil || got.Champion.Hash != "h2" {
		t.Fatalf("after rollback champion=%+v want hash=h2", got.Champion)
	}
}

func testRollbackWithoutHistory(t *testing.T, mk Factory) {
	s, _ := mk(t)
	if _, err := s.PromoteChampion(ctx(), "production", store.Hash("h1"), "alice", ""); err != nil {
		t.Fatal(err)
	}
	_, err := s.RollbackChampion(ctx(), "production", "alice", "")
	if !errors.Is(err, envstate.ErrNoPreviousChampion) {
		t.Fatalf("err=%v want ErrNoPreviousChampion", err)
	}
}

func testRollbackWithoutChampion(t *testing.T, mk Factory) {
	s, _ := mk(t)
	_, err := s.RollbackChampion(ctx(), "production", "alice", "")
	if !errors.Is(err, envstate.ErrNoChampion) {
		t.Fatalf("err=%v want ErrNoChampion", err)
	}
}

// testGetDeepCopiesAfterPromote confirms cloneState is exercised on
// the Writer path, not only after direct seed injection — the seed-
// path version (testGetDeepCopiesRole) only covers the cloneState in
// Get; this one proves Promote-then-Get also clones.
func testGetDeepCopiesAfterPromote(t *testing.T, mk Factory) {
	s, _ := mk(t)
	if _, err := s.PromoteChampion(ctx(), "production", store.Hash("h1"), "alice", ""); err != nil {
		t.Fatal(err)
	}
	first, _ := s.Get(ctx(), "production")
	first.Champion.PromotedBy = "MUTATED"

	second, _ := s.Get(ctx(), "production")
	if second.Champion.PromotedBy != "alice" {
		t.Fatalf("mutation bled through deep-copy: %+v", second.Champion)
	}
}

func testChallengerStubbed(t *testing.T, mk Factory) {
	s, _ := mk(t)
	for _, call := range []struct {
		name string
		fn   func() error
	}{
		{"PromoteChallenger", func() error { return s.PromoteChallenger(ctx(), "p", store.Hash("h"), "op", "r") }},
		{"RejectChallenger", func() error { return s.RejectChallenger(ctx(), "p", "op", "r") }},
	} {
		if err := call.fn(); !errors.Is(err, envstate.ErrNotImplemented) {
			t.Fatalf("%s: err=%v want ErrNotImplemented", call.name, err)
		}
	}
}
