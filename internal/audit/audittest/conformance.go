// Package audittest is the reusable contract suite every audit
// backing must satisfy. SeedFunc lets the suite pre-populate a
// backing without going through the stubbed Writer.
package audittest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/audit"
)

// SeedFunc bypasses the Writer projection so Reader cases can run
// against a known fixture. Every backing supplies one; the suite
// fails loudly when it is nil.
type SeedFunc func(entries []audit.Entry)

// Factory builds a fresh audit.Store + SeedFunc.
type Factory func(t *testing.T) (audit.Store, SeedFunc)

// RunConformance exercises every behaviour audit.Reader promises.
// Writer cases assert ErrNotImplemented until ADR-0005 lands real
// implementations.
func RunConformance(t *testing.T, factory Factory) {
	t.Helper()
	cases := []struct {
		name string
		fn   func(t *testing.T, factory Factory)
	}{
		{"ListEmptyOnFreshStore", testListEmpty},
		{"ListOrdersByAtDescending", testListOrdersByAtDesc},
		{"ListPaginatesViaCursor", testListPaginates},
		{"ListUnknownCursorRestarts", testListUnknownCursor},
		{"ListLimitDefaultsAndCaps", testListLimitClamping},
		{"RecordReturnsErrNotImplemented", testRecordStubbed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.fn(t, factory) })
	}
}

func ctx() context.Context { return context.Background() }

func testListEmpty(t *testing.T, mk Factory) {
	s, _ := mk(t)
	page, err := s.List(ctx(), audit.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatalf("fresh store should be empty: %+v", page)
	}
}

func testListOrdersByAtDesc(t *testing.T, mk Factory) {
	s, seed := mk(t)
	if seed == nil {
		t.Fatal("conformance requires SeedFunc; backing must provide one")
	}
	seed([]audit.Entry{
		{ID: "a", Action: "promote", At: time.Unix(1, 0).UTC()},
		{ID: "b", Action: "promote", At: time.Unix(2, 0).UTC()},
		{ID: "c", Action: "promote", At: time.Unix(3, 0).UTC()},
	})
	page, err := s.List(ctx(), audit.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 || page.Items[0].ID != "c" || page.Items[2].ID != "a" {
		t.Fatalf("newest-first order broken: %+v", page.Items)
	}
}

func testListPaginates(t *testing.T, mk Factory) {
	s, seed := mk(t)
	if seed == nil {
		t.Fatal("conformance requires SeedFunc; backing must provide one")
	}
	seed([]audit.Entry{
		{ID: "a", At: time.Unix(1, 0).UTC()},
		{ID: "b", At: time.Unix(2, 0).UTC()},
		{ID: "c", At: time.Unix(3, 0).UTC()},
	})
	p1, err := s.List(ctx(), audit.ListOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Items) != 2 || p1.NextCursor == "" {
		t.Fatalf("first page: items=%d next=%q", len(p1.Items), p1.NextCursor)
	}
	p2, err := s.List(ctx(), audit.ListOptions{Limit: 2, Cursor: p1.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Items) != 1 || p2.NextCursor != "" {
		t.Fatalf("second page: items=%d next=%q", len(p2.Items), p2.NextCursor)
	}
}

func testListUnknownCursor(t *testing.T, mk Factory) {
	s, seed := mk(t)
	if seed == nil {
		t.Fatal("conformance requires SeedFunc; backing must provide one")
	}
	seed([]audit.Entry{
		{ID: "a", At: time.Unix(1, 0).UTC()},
	})
	page, err := s.List(ctx(), audit.ListOptions{Cursor: "no-such-cursor"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("unknown cursor should restart; got %d items", len(page.Items))
	}
}

func testListLimitClamping(t *testing.T, mk Factory) {
	s, seed := mk(t)
	if seed == nil {
		t.Fatal("conformance requires SeedFunc; backing must provide one")
	}
	seed([]audit.Entry{
		{ID: "a", At: time.Unix(1, 0).UTC()},
		{ID: "b", At: time.Unix(2, 0).UTC()},
	})
	page, err := s.List(ctx(), audit.ListOptions{Limit: -5})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("negative limit should default, got %d", len(page.Items))
	}
	page, err = s.List(ctx(), audit.ListOptions{Limit: 9999})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("oversize limit should cap silently, got %d", len(page.Items))
	}
}

func testRecordStubbed(t *testing.T, mk Factory) {
	s, _ := mk(t)
	if err := s.Record(ctx(), audit.Entry{ID: "x"}); !errors.Is(err, audit.ErrNotImplemented) {
		t.Fatalf("Record: err=%v want ErrNotImplemented", err)
	}
}
