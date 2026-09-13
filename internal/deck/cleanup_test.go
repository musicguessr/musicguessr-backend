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

func TestCleanupExpired_SkipsNamespacedCacheKeys(t *testing.T) {
	// Deck storage and internal/rcache share the same S3 bucket — rcache
	// entries look like "youtube/<hash>" and must never be fetched or
	// treated as a (corrupted) deck.
	store := newMockStore()
	_ = store.Put(context.Background(), "youtube/abc123hash", []byte(`"dQw4w9WgXcQ"`))
	putDeck(t, store, "live", time.Now().Add(time.Hour))

	deleted, err := CleanupExpired(context.Background(), store)
	if err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("got %d deleted, want 0", deleted)
	}
	if _, ok := store.data["youtube/abc123hash"]; !ok {
		t.Error("namespaced cache key should be left alone, not deleted")
	}
}

func TestCleanupExpired_NeverDeletesForeignObjects(t *testing.T) {
	store := newMockStore()
	ctx := context.Background()
	// Legacy flat rcache keys from before the "/" namespace separator.
	_ = store.Put(ctx, "metadata-3f2a9c", []byte(`{"artist":"Mabel","title":"Don't Call Me Up","year":2019}`))
	_ = store.Put(ctx, "youtube-9b1e44", []byte(`"bkCoJA4CgRY"`))
	// Valid-looking ID whose JSON parses but isn't a deck.
	_ = store.Put(ctx, "abc123", []byte(`{"artist":"Mabel"}`))
	// Deck JSON stored under a different key than its own ID.
	data, _ := json.Marshal(Deck{ID: "other", ExpiresAt: time.Now().Add(-time.Hour)})
	_ = store.Put(ctx, "mismatch", data)

	deleted, err := CleanupExpired(ctx, store)
	if err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("got %d deleted, want 0", deleted)
	}
	for _, key := range []string{"metadata-3f2a9c", "youtube-9b1e44", "abc123", "mismatch"} {
		if _, ok := store.data[key]; !ok {
			t.Errorf("%q should not have been deleted", key)
		}
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
