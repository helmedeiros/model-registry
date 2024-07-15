package fsstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/helmedeiros/model-registry/internal/store"
)

func TestGetBundleReturnsMetadataOnlyWithFlags(t *testing.T) {
	s := newFsstore(t)
	h, err := s.Put(context.Background(), store.PutRequest{
		SourceBytes:   []byte("src"),
		ContentType:   store.ContentTypeCSV,
		SnapshotBytes: []byte("snap"),
		Metadata:      store.Metadata{CreatedBy: "t", Description: "d"},
	})
	if err != nil {
		t.Fatal(err)
	}
	bun, err := s.GetBundle(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if bun.Hash != h {
		t.Fatalf("Hash=%s want %s", bun.Hash, h)
	}
	if bun.State != store.StateStaged {
		t.Fatalf("State=%s want staged", bun.State)
	}
	if !bun.HasSnapshot || bun.HasDiagnose {
		t.Fatalf("HasSnapshot=%v HasDiagnose=%v", bun.HasSnapshot, bun.HasDiagnose)
	}
	if bun.Metadata.CreatedBy != "t" || bun.Metadata.Description != "d" {
		t.Fatalf("metadata round-trip lost fields: %+v", bun.Metadata)
	}
}

func TestGetBundleUnknownHashReturnsNotFound(t *testing.T) {
	s := newFsstore(t)
	_, err := s.GetBundle(context.Background(), store.Hash("nope"))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGetMemberDispatchesByKind(t *testing.T) {
	s := newFsstore(t)
	h, err := s.Put(context.Background(), store.PutRequest{
		SourceBytes:   []byte("S"),
		ContentType:   store.ContentTypeCSV,
		SnapshotBytes: []byte("N"),
		DiagnoseBytes: []byte("D"),
	})
	if err != nil {
		t.Fatal(err)
	}
	src, ct, err := s.GetMember(context.Background(), h, store.MemberSource)
	if err != nil || string(src) != "S" || ct != store.ContentTypeCSV {
		t.Fatalf("MemberSource: bytes=%q ct=%q err=%v", src, ct, err)
	}
	snap, ct, err := s.GetMember(context.Background(), h, store.MemberSnapshot)
	if err != nil || string(snap) != "N" || ct != store.ContentTypeUnknown {
		t.Fatalf("MemberSnapshot: bytes=%q ct=%q err=%v", snap, ct, err)
	}
	diag, ct, err := s.GetMember(context.Background(), h, store.MemberDiagnose)
	if err != nil || string(diag) != "D" || ct != store.ContentTypeUnknown {
		t.Fatalf("MemberDiagnose: bytes=%q ct=%q err=%v", diag, ct, err)
	}
}

func TestGetMemberAbsentForUnuploadedDerived(t *testing.T) {
	s := newFsstore(t)
	h, err := s.Put(context.Background(), store.PutRequest{
		SourceBytes: []byte("only-source"),
		ContentType: store.ContentTypeCSV,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetMember(context.Background(), h, store.MemberSnapshot); !errors.Is(err, store.ErrMemberAbsent) {
		t.Fatalf("expected ErrMemberAbsent for snapshot, got %v", err)
	}
	if _, _, err := s.GetMember(context.Background(), h, store.MemberDiagnose); !errors.Is(err, store.ErrMemberAbsent) {
		t.Fatalf("expected ErrMemberAbsent for diagnose, got %v", err)
	}
}

func TestGetMemberUnknownHashReturnsNotFound(t *testing.T) {
	s := newFsstore(t)
	if _, _, err := s.GetMember(context.Background(), store.Hash("nope"), store.MemberSource); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGetMemberRejectsUnknownKind(t *testing.T) {
	s := newFsstore(t)
	h, err := s.Put(context.Background(), store.PutRequest{
		SourceBytes: []byte("x"),
		ContentType: store.ContentTypeCSV,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetMember(context.Background(), h, store.MemberKind("rogue")); !errors.Is(err, store.ErrMemberAbsent) {
		t.Fatalf("expected ErrMemberAbsent for rogue kind, got %v", err)
	}
}
