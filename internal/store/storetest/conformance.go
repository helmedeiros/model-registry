// Package storetest is the contract suite every store.Store backing
// runs against. A new backing implements its tests as
//
//	func TestConformance(t *testing.T) {
//	    storetest.RunConformance(t, factory)
//	}
//
// and inherits the full lifecycle, idempotency, error-sentinel, and
// pagination coverage.
package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/store"
)

// Factory builds a fresh store.Store wired to the supplied clock. The
// clock is monotonic-by-1ms-per-call so created_at ordering is
// deterministic across backings. The *testing.T is the subtest's T;
// backings that need teardown (tempdir, sql handle, etc.) register it
// via t.Cleanup so it scopes to that subtest.
type Factory func(t *testing.T, clock func() time.Time) store.Store

// RunConformance exercises every behavior the store.Store contract
// promises. Each subtest is independent — a backing that fails one does
// not abort the rest.
func RunConformance(t *testing.T, factory Factory) {
	t.Helper()
	tests := []struct {
		name string
		fn   func(t *testing.T, mk func(*testing.T) store.Store)
	}{
		{"PutValidatesRequiredFields", testPutValidatesRequiredFields},
		{"PutIsIdempotentOnSameSourceBytes", testPutIsIdempotentOnSameSourceBytes},
		{"PutPreservesByteIndependenceFromInputSlices", testPutPreservesByteIndependence},
		{"GetBundleReturnsMetadataOnlyWithMemberFlags", testGetBundleMetadataOnly},
		{"GetBundleUnknownHashReturnsNotFound", testGetBundleNotFound},
		{"GetMemberDispatchesByMemberKind", testGetMemberDispatchesByKind},
		{"GetMemberAbsentForUnuploadedDerivedMember", testGetMemberAbsentForUnuploaded},
		{"GetMemberUnknownHashReturnsNotFound", testGetMemberNotFound},
		{"GetMemberRejectsUnknownMemberKind", testGetMemberRejectsUnknownKind},
		{"TagTransitionsStagedToActiveAndRecordsHead", testTagStagedToActive},
		{"TagRePointingMovesHead", testTagRePointingMovesHead},
		{"TagUnknownHashReturnsNotFound", testTagUnknownHash},
		{"TagDeprecatedArtifactInvalidTransition", testTagDeprecatedInvalidTransition},
		{"ResolveTagUnknownReturnsErr", testResolveTagUnknown},
		{"ListTagsReturnsCurrentHeads", testListTagsHeads},
		{"DeprecateFromStagedIsTerminal", testDeprecateFromStaged},
		{"DeprecateFromActiveIsTerminal", testDeprecateFromActive},
		{"DeprecateUnknownHashReturnsNotFound", testDeprecateUnknown},
		{"ListOrdersByCreatedAtDescAndPaginates", testListOrdersAndPaginates},
		{"ListAppliesStateFilter", testListStateFilter},
		{"ListLimitDefaultsAndCaps", testListLimits},
		{"ListCursorUnknownStartsFromBeginning", testListUnknownCursor},
		{"ListTieBreaksByHashWhenCreatedAtEqual", testListTieBreaksAscending},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mk := func(t *testing.T) store.Store {
				clk := newFakeClock(time.Unix(0, 0))
				return factory(t, clk.Now)
			}
			tc.fn(t, mk)
		})
	}
}

// --- helpers shared across cases ---

func ctx() context.Context { return context.Background() }

type fakeClock struct {
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{now: start} }

func (c *fakeClock) Now() time.Time {
	c.now = c.now.Add(time.Millisecond)
	return c.now
}

func putCSV(t *testing.T, s store.Store, name string) store.Hash {
	t.Helper()
	h, err := s.Put(ctx(), store.PutRequest{
		SourceBytes: []byte("rule=" + name),
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedBy: "tester", Description: name},
	})
	if err != nil {
		t.Fatalf("Put(%s): %v", name, err)
	}
	return h
}

// --- test bodies ---

func testPutValidatesRequiredFields(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	if _, err := s.Put(ctx(), store.PutRequest{ContentType: store.ContentTypeCSV}); !errors.Is(err, store.ErrSourceRequired) {
		t.Fatalf("expected ErrSourceRequired, got %v", err)
	}
	if _, err := s.Put(ctx(), store.PutRequest{SourceBytes: []byte("x")}); !errors.Is(err, store.ErrContentTypeRequired) {
		t.Fatalf("expected ErrContentTypeRequired, got %v", err)
	}
}

func testPutIsIdempotentOnSameSourceBytes(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	req := store.PutRequest{
		SourceBytes:   []byte("same-bytes"),
		ContentType:   store.ContentTypeCSV,
		SnapshotBytes: []byte("first-snapshot"),
		Metadata:      store.Metadata{CreatedBy: "first"},
	}
	h1, err := s.Put(ctx(), req)
	if err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	h2, err := s.Put(ctx(), store.PutRequest{
		SourceBytes:   []byte("same-bytes"),
		ContentType:   store.ContentTypeCSV,
		SnapshotBytes: []byte("second-snapshot-ignored"),
		Metadata:      store.Metadata{CreatedBy: "second-ignored"},
	})
	if err != nil {
		t.Fatalf("Put 2: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("re-Put returned different hash: %s vs %s", h1, h2)
	}
	bun, err := s.GetBundle(ctx(), h1)
	if err != nil {
		t.Fatalf("GetBundle: %v", err)
	}
	if bun.Metadata.CreatedBy != "first" {
		t.Fatalf("re-Put overwrote metadata: %q", bun.Metadata.CreatedBy)
	}
	body, _, err := s.GetMember(ctx(), h1, store.MemberSnapshot)
	if err != nil {
		t.Fatalf("GetMember snapshot: %v", err)
	}
	if string(body) != "first-snapshot" {
		t.Fatalf("re-Put overwrote snapshot bytes: %q", body)
	}
}

func testPutPreservesByteIndependence(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	source := []byte("original")
	h, err := s.Put(ctx(), store.PutRequest{SourceBytes: source, ContentType: store.ContentTypeCSV})
	if err != nil {
		t.Fatal(err)
	}
	source[0] = 'X'
	got, _, err := s.GetMember(ctx(), h, store.MemberSource)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("Store retained reference to caller's slice: %q", got)
	}
}

func testGetBundleMetadataOnly(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	h, err := s.Put(ctx(), store.PutRequest{
		SourceBytes:   []byte("src"),
		ContentType:   store.ContentTypeCSV,
		SnapshotBytes: []byte("snap"),
	})
	if err != nil {
		t.Fatal(err)
	}
	bun, err := s.GetBundle(ctx(), h)
	if err != nil {
		t.Fatalf("GetBundle: %v", err)
	}
	if !bun.HasSnapshot || bun.HasDiagnose {
		t.Fatalf("HasSnapshot=%v HasDiagnose=%v", bun.HasSnapshot, bun.HasDiagnose)
	}
	if bun.State != store.StateStaged {
		t.Fatalf("freshly-put artifact should be staged, got %s", bun.State)
	}
}

func testGetBundleNotFound(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	_, err := s.GetBundle(ctx(), store.Hash("does-not-exist"))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func testGetMemberDispatchesByKind(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	h, _ := s.Put(ctx(), store.PutRequest{
		SourceBytes:   []byte("S"),
		ContentType:   store.ContentTypeCSV,
		SnapshotBytes: []byte("N"),
		DiagnoseBytes: []byte("D"),
	})
	src, ct, err := s.GetMember(ctx(), h, store.MemberSource)
	if err != nil || string(src) != "S" || ct != store.ContentTypeCSV {
		t.Fatalf("MemberSource: bytes=%q ct=%q err=%v", src, ct, err)
	}
	snap, ct, err := s.GetMember(ctx(), h, store.MemberSnapshot)
	if err != nil || string(snap) != "N" || ct != store.ContentTypeUnknown {
		t.Fatalf("MemberSnapshot: bytes=%q ct=%q err=%v", snap, ct, err)
	}
	diag, ct, err := s.GetMember(ctx(), h, store.MemberDiagnose)
	if err != nil || string(diag) != "D" || ct != store.ContentTypeUnknown {
		t.Fatalf("MemberDiagnose: bytes=%q ct=%q err=%v", diag, ct, err)
	}
}

func testGetMemberAbsentForUnuploaded(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	h, _ := s.Put(ctx(), store.PutRequest{SourceBytes: []byte("only-source"), ContentType: store.ContentTypeCSV})
	if _, _, err := s.GetMember(ctx(), h, store.MemberSnapshot); !errors.Is(err, store.ErrMemberAbsent) {
		t.Fatalf("expected ErrMemberAbsent for snapshot, got %v", err)
	}
	if _, _, err := s.GetMember(ctx(), h, store.MemberDiagnose); !errors.Is(err, store.ErrMemberAbsent) {
		t.Fatalf("expected ErrMemberAbsent for diagnose, got %v", err)
	}
}

func testGetMemberNotFound(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	if _, _, err := s.GetMember(ctx(), store.Hash("nope"), store.MemberSource); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func testGetMemberRejectsUnknownKind(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	h, _ := s.Put(ctx(), store.PutRequest{SourceBytes: []byte("x"), ContentType: store.ContentTypeCSV})
	if _, _, err := s.GetMember(ctx(), h, store.MemberKind("rogue")); !errors.Is(err, store.ErrMemberAbsent) {
		t.Fatalf("expected ErrMemberAbsent for rogue member kind, got %v", err)
	}
}

func testTagStagedToActive(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	h := putCSV(t, s, "alpha")
	if err := s.Tag(ctx(), "v1", h); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	bun, _ := s.GetBundle(ctx(), h)
	if bun.State != store.StateActive {
		t.Fatalf("Tag did not transition staged->active: %s", bun.State)
	}
	got, err := s.ResolveTag(ctx(), "v1")
	if err != nil || got != h {
		t.Fatalf("ResolveTag(v1)=%s err=%v want=%s", got, err, h)
	}
}

func testTagRePointingMovesHead(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	h1 := putCSV(t, s, "v1")
	h2 := putCSV(t, s, "v2")
	if err := s.Tag(ctx(), "release", h1); err != nil {
		t.Fatal(err)
	}
	if err := s.Tag(ctx(), "release", h2); err != nil {
		t.Fatal(err)
	}
	got, err := s.ResolveTag(ctx(), "release")
	if err != nil || got != h2 {
		t.Fatalf("ResolveTag(release)=%s err=%v want=%s", got, err, h2)
	}
}

func testTagUnknownHash(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	if err := s.Tag(ctx(), "v1", store.Hash("missing")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func testTagDeprecatedInvalidTransition(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	h := putCSV(t, s, "alpha")
	if err := s.Deprecate(ctx(), h, "obsolete"); err != nil {
		t.Fatal(err)
	}
	if err := s.Tag(ctx(), "v1", h); !errors.Is(err, store.ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}
}

func testResolveTagUnknown(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	if _, err := s.ResolveTag(ctx(), "never-assigned"); !errors.Is(err, store.ErrTagUnknown) {
		t.Fatalf("expected ErrTagUnknown, got %v", err)
	}
}

func testListTagsHeads(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	h1 := putCSV(t, s, "first")
	h2 := putCSV(t, s, "second")
	if err := s.Tag(ctx(), "a", h1); err != nil {
		t.Fatal(err)
	}
	if err := s.Tag(ctx(), "b", h2); err != nil {
		t.Fatal(err)
	}
	heads, err := s.ListTags(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if heads["a"] != h1 || heads["b"] != h2 || len(heads) != 2 {
		t.Fatalf("unexpected heads: %v", heads)
	}
}

func testDeprecateFromStaged(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	h := putCSV(t, s, "alpha")
	if err := s.Deprecate(ctx(), h, "withdrawn"); err != nil {
		t.Fatalf("Deprecate: %v", err)
	}
	bun, _ := s.GetBundle(ctx(), h)
	if bun.State != store.StateDeprecated {
		t.Fatalf("state=%s want deprecated", bun.State)
	}
	if err := s.Deprecate(ctx(), h, "again"); !errors.Is(err, store.ErrInvalidTransition) {
		t.Fatalf("re-deprecation should fail, got %v", err)
	}
}

func testDeprecateFromActive(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	h := putCSV(t, s, "alpha")
	if err := s.Tag(ctx(), "v1", h); err != nil {
		t.Fatal(err)
	}
	if err := s.Deprecate(ctx(), h, "rolled out"); err != nil {
		t.Fatalf("Deprecate: %v", err)
	}
	bun, _ := s.GetBundle(ctx(), h)
	if bun.State != store.StateDeprecated {
		t.Fatalf("state=%s want deprecated", bun.State)
	}
}

func testDeprecateUnknown(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	if err := s.Deprecate(ctx(), store.Hash("missing"), "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func testListOrdersAndPaginates(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	h1 := putCSV(t, s, "first")
	h2 := putCSV(t, s, "second")
	h3 := putCSV(t, s, "third")

	page, err := s.List(ctx(), store.ListOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].Hash != h3 || page.Items[1].Hash != h2 {
		t.Fatalf("unexpected first page: %+v", page.Items)
	}
	if page.NextCursor == "" {
		t.Fatalf("expected NextCursor for non-final page")
	}

	page2, err := s.List(ctx(), store.ListOptions{Limit: 2, Cursor: page.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 1 || page2.Items[0].Hash != h1 {
		t.Fatalf("unexpected second page: %+v", page2.Items)
	}
	if page2.NextCursor != "" {
		t.Fatalf("expected empty NextCursor on final page, got %q", page2.NextCursor)
	}
}

func testListStateFilter(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	hStaged := putCSV(t, s, "staged-one")
	hActive := putCSV(t, s, "active-one")
	if err := s.Tag(ctx(), "head", hActive); err != nil {
		t.Fatal(err)
	}

	page, err := s.List(ctx(), store.ListOptions{State: store.StateActive})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Hash != hActive {
		t.Fatalf("state-filtered page: %+v", page.Items)
	}

	page, err = s.List(ctx(), store.ListOptions{State: store.StateStaged})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Hash != hStaged {
		t.Fatalf("staged-filtered page: %+v", page.Items)
	}
}

func testListLimits(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	for i := 0; i < 3; i++ {
		putCSV(t, s, string(rune('a'+i)))
	}
	page, err := s.List(ctx(), store.ListOptions{Limit: -5})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("negative limit should default, got %d items", len(page.Items))
	}
	page, err = s.List(ctx(), store.ListOptions{Limit: 9999})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("oversized limit should cap silently, got %d items", len(page.Items))
	}
}

func testListUnknownCursor(t *testing.T, mk func(*testing.T) store.Store) {
	s := mk(t)
	putCSV(t, s, "alpha")
	page, err := s.List(ctx(), store.ListOptions{Cursor: "no-such-hash"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("unknown cursor should fall through; got %d items", len(page.Items))
	}
}

func testListTieBreaksAscending(t *testing.T, mk func(*testing.T) store.Store) {
	// ADR-0002: ascending hash order when created_at is equal.
	s := mk(t)
	same := time.Unix(1_700_000_000, 0).UTC()
	h1, err := s.Put(ctx(), store.PutRequest{
		SourceBytes: []byte("alpha"),
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedAt: same, CreatedBy: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	h2, err := s.Put(ctx(), store.PutRequest{
		SourceBytes: []byte("bravo"),
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedAt: same, CreatedBy: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}

	page, err := s.List(ctx(), store.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("want 2 items, got %d", len(page.Items))
	}
	low, high := h1, h2
	if h2 < h1 {
		low, high = h2, h1
	}
	if page.Items[0].Hash != low || page.Items[1].Hash != high {
		t.Fatalf("tie-break order broken: %s,%s want %s,%s", page.Items[0].Hash, page.Items[1].Hash, low, high)
	}
}
