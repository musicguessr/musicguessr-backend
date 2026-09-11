package deck

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func putDeck(t *testing.T, store *mockStore, id string, expiresAt time.Time) {
	t.Helper()
	data, err := json.Marshal(Deck{ID: id, ExpiresAt: expiresAt})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := store.Put(context.Background(), id, data); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

func TestCleanupExpired_DeletesOnlyExpired(t *testing.T) {
	store := newMockStore()
	putDeck(t, store, "expired", time.Now().Add(-time.Hour))
	putDeck(t, store, "live", time.Now().Add(time.Hour))

	deleted, err := CleanupExpired(context.Background(), store)
	if err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("got %d deleted, want 1", deleted)
	}
	if _, ok := store.data["expired"]; ok {
		t.Error("expired deck should have been deleted")
	}
	if _, ok := store.data["live"]; !ok {
		t.Error("live deck should not have been deleted")
	}
}

func TestCleanupExpired_SkipsCorruptedEntries(t *testing.T) {
	store := newMockStore()
	_ = store.Put(context.Background(), "corrupted", []byte("not json"))

	deleted, err := CleanupExpired(context.Background(), store)
	if err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("got %d deleted, want 0", deleted)
	}
	if _, ok := store.data["corrupted"]; !ok {
		t.Error("corrupted entry should be left alone, not deleted")
	}
}

func TestCleanupExpired_EmptyStore(t *testing.T) {
	store := newMockStore()
	deleted, err := CleanupExpired(context.Background(), store)
	if err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("got %d deleted, want 0", deleted)
	}
}
