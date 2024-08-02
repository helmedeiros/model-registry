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

// RunConformance exercises every behaviour audit.Reader and Writer
// promise. Writer cases verify append, duplicate rejection, and
// required-field validation.
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
		{"RecordAppendsEntry", testRecordAppendsEntry},
		{"RecordRefusesDuplicateID", testRecordRefusesDuplicateID},
		{"RecordValidatesRequiredFields", testRecordValidation},
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

func sampleEntry(id string, at time.Time) audit.Entry {
	return audit.Entry{
		ID:       id,
		Operator: "alice",
		Action:   "promote",
		Target:   "env/production/champion",
		At:       at,
	}
}

func testRecordAppendsEntry(t *testing.T, mk Factory) {
	s, _ := mk(t)
	entry := sampleEntry("01HXY00000000000000000000A", time.Unix(1_700_000_000, 0).UTC())
	if err := s.Record(ctx(), entry); err != nil {
		t.Fatalf("Record: %v", err)
	}
	page, err := s.List(ctx(), audit.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != entry.ID {
		t.Fatalf("List after Record: %+v", page)
	}
}

func testRecordRefusesDuplicateID(t *testing.T, mk Factory) {
	s, _ := mk(t)
	entry := sampleEntry("01HXY00000000000000000000A", time.Unix(1_700_000_000, 0).UTC())
	if err := s.Record(ctx(), entry); err != nil {
		t.Fatal(err)
	}
	// Vary every non-ID field to prove the check discriminates on ID
	// alone — a future change that hashed the full entry would pass
	// the previous version of this test silently.
	dup := audit.Entry{
		ID:       entry.ID,
		Operator: "bob",
		Action:   "rollback",
		Target:   "env/staging/champion",
		At:       entry.At.Add(time.Second),
	}
	if err := s.Record(ctx(), dup); !errors.Is(err, audit.ErrDuplicateID) {
		t.Fatalf("Record dup: err=%v want ErrDuplicateID", err)
	}
}

func testRecordValidation(t *testing.T, mk Factory) {
	at := time.Unix(1_700_000_000, 0).UTC()
	for _, tc := range []struct {
		name  string
		entry audit.Entry
		want  error
	}{
		{"empty id", audit.Entry{Operator: "a", Action: "promote", Target: "t", At: at}, audit.ErrIDRequired},
		{"empty operator", audit.Entry{ID: "x", Action: "promote", Target: "t", At: at}, audit.ErrOperatorRequired},
		{"empty action", audit.Entry{ID: "x", Operator: "a", Target: "t", At: at}, audit.ErrActionRequired},
		{"empty target", audit.Entry{ID: "x", Operator: "a", Action: "promote", At: at}, audit.ErrTargetRequired},
		{"zero at", audit.Entry{ID: "x", Operator: "a", Action: "promote", Target: "t"}, audit.ErrAtRequired},
	} {
		s, _ := mk(t)
		if err := s.Record(ctx(), tc.entry); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err=%v want %v", tc.name, err, tc.want)
		}
	}
}
