package deckstore

import (
	"context"
	"sort"
	"testing"
)

func TestLocalStore_PutGetDeleteList(t *testing.T) {
	dir := t.TempDir()
	s, err := newLocal(dir)
	if err != nil {
		t.Fatalf("newLocal: %v", err)
	}
	ctx := context.Background()

	if err := s.Put(ctx, "abc", []byte(`{"id":"abc"}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Put(ctx, "def", []byte(`{"id":"def"}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	data, err := s.Get(ctx, "abc")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(data) != `{"id":"abc"}` {
		t.Fatalf("Get returned %q", data)
	}

	ids, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sort.Strings(ids)
	if len(ids) != 2 || ids[0] != "abc" || ids[1] != "def" {
		t.Fatalf("List returned %v", ids)
	}

	if err := s.Delete(ctx, "abc"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, "abc"); err != ErrNotFound {
		t.Fatalf("Get after Delete: got err %v, want ErrNotFound", err)
	}

	// Deleting an id that no longer exists must not error — the expiry
	// cleanup job relies on Delete being idempotent.
	if err := s.Delete(ctx, "abc"); err != nil {
		t.Fatalf("Delete of already-deleted id: %v", err)
	}

	ids, err = s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ids) != 1 || ids[0] != "def" {
		t.Fatalf("List after Delete returned %v", ids)
	}
}

func TestLocalStore_PutGetDelete_NamespacedID(t *testing.T) {
	// rcache keys look like "metadata/<hash>" — the namespace subdirectory
	// doesn't exist ahead of time and must be created on demand.
	dir := t.TempDir()
	s, err := newLocal(dir)
	if err != nil {
		t.Fatalf("newLocal: %v", err)
	}
	ctx := context.Background()

	id := "metadata/abc123hash"
	if err := s.Put(ctx, id, []byte(`{"artist":"Test"}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	data, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(data) != `{"artist":"Test"}` {
		t.Fatalf("Get returned %q", data)
	}

	// A second key under the same namespace must work too — the mkdir isn't
	// a one-shot fluke tied to the first write.
	id2 := "metadata/def456hash"
	if err := s.Put(ctx, id2, []byte(`{"artist":"Other"}`)); err != nil {
		t.Fatalf("Put second namespaced id: %v", err)
	}

	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, id); err != ErrNotFound {
		t.Fatalf("Get after Delete: got err %v, want ErrNotFound", err)
	}
}
