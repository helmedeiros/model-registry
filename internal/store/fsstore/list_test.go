package fsstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/store"
)

func TestListReturnsCreatedAtDescending(t *testing.T) {
	s := newFsstore(t)
	h1 := putRule(t, s, "alpha")
	h2 := putRule(t, s, "beta")
	h3 := putRule(t, s, "gamma")

	page, err := s.List(context.Background(), store.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("want 3 items, got %d", len(page.Items))
	}
	// putRule advances the test clock between Puts so h3 is newest.
	if page.Items[0].Hash != h3 || page.Items[1].Hash != h2 || page.Items[2].Hash != h1 {
		t.Fatalf("ordering broken: %s,%s,%s want %s,%s,%s",
			page.Items[0].Hash, page.Items[1].Hash, page.Items[2].Hash, h3, h2, h1)
	}
}

func TestListPaginatesWithCursor(t *testing.T) {
	s := newFsstore(t)
	h1 := putRule(t, s, "alpha")
	h2 := putRule(t, s, "beta")
	h3 := putRule(t, s, "gamma")

	page, err := s.List(context.Background(), store.ListOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].Hash != h3 || page.Items[1].Hash != h2 {
		t.Fatalf("first page: %+v", page.Items)
	}
	if page.NextCursor == "" {
		t.Fatalf("expected NextCursor for non-final page")
	}

	page2, err := s.List(context.Background(), store.ListOptions{Limit: 2, Cursor: page.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 1 || page2.Items[0].Hash != h1 {
		t.Fatalf("second page: %+v", page2.Items)
	}
	if page2.NextCursor != "" {
		t.Fatalf("expected empty NextCursor on final page, got %q", page2.NextCursor)
	}
}

func TestListStateFilter(t *testing.T) {
	s := newFsstore(t)
	hStaged := putRule(t, s, "staged-one")
	hActive := putRule(t, s, "active-one")
	if err := s.Tag(context.Background(), "head", hActive); err != nil {
		t.Fatal(err)
	}

	page, err := s.List(context.Background(), store.ListOptions{State: store.StateActive})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Hash != hActive {
		t.Fatalf("active-filtered: %+v", page.Items)
	}

	page, err = s.List(context.Background(), store.ListOptions{State: store.StateStaged})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Hash != hStaged {
		t.Fatalf("staged-filtered: %+v", page.Items)
	}
}

func TestListLimitsClampedToDefaultsAndMax(t *testing.T) {
	s := newFsstore(t)
	for _, name := range []string{"a", "b", "c"} {
		putRule(t, s, name)
	}
	page, err := s.List(context.Background(), store.ListOptions{Limit: -5})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("negative limit should default, got %d items", len(page.Items))
	}
	page, err = s.List(context.Background(), store.ListOptions{Limit: 9999})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("oversized limit should cap silently, got %d items", len(page.Items))
	}
}

func TestListUnknownCursorRestartsTraversal(t *testing.T) {
	s := newFsstore(t)
	putRule(t, s, "alpha")

	page, err := s.List(context.Background(), store.ListOptions{Cursor: "no-such-hash"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("unknown cursor should restart, got %d items", len(page.Items))
	}
}

func TestListTieBreaksAscending(t *testing.T) {
	// ADR-0002: ascending hash order when created_at is equal. fsstore
	// stores created_at at millisecond resolution; this test pins both
	// artifacts to the same instant so the tiebreak is exercised.
	s := newFsstore(t)
	same := time.Unix(1_700_000_000, 0).UTC()
	h1, err := s.Put(context.Background(), store.PutRequest{
		SourceBytes: []byte("alpha"),
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedAt: same, CreatedBy: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	h2, err := s.Put(context.Background(), store.PutRequest{
		SourceBytes: []byte("bravo"),
		ContentType: store.ContentTypeCSV,
		Metadata:    store.Metadata{CreatedAt: same, CreatedBy: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}

	page, err := s.List(context.Background(), store.ListOptions{})
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
		t.Fatalf("tie-break broken: %s,%s want %s,%s",
			page.Items[0].Hash, page.Items[1].Hash, low, high)
	}
}
