package deck

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/musicguessr/musicguessr-backend/internal/deckstore"
)

// cleanupConcurrency bounds how many decks are fetched from the store at
// once during a sweep — same pattern as CreateDeck's enrichConcurrency, so a
// sweep over many decks doesn't open unbounded concurrent connections to the
// store backend (S3 or local disk).
const cleanupConcurrency = 10

// CleanupExpired deletes every deck in store whose ExpiresAt has passed.
// GetDeck already refuses to serve an expired deck (410 Gone), so this isn't
// needed for correctness — it exists because deckstore has no TTL of its
// own: without this, an expired deck's JSON just sits in S3/on disk forever,
// permanently growing storage (and, on paid S3-compatible providers, cost)
// for an object nothing can ever serve again.
func CleanupExpired(ctx context.Context, store deckstore.Store) (deleted int, err error) {
	ids, err := store.List(ctx)
	if err != nil {
		return 0, err
	}

	var (
		mu    sync.Mutex
		sem   = make(chan struct{}, cleanupConcurrency)
		wg    sync.WaitGroup
		count int
	)
	now := time.Now().UTC()

	for _, id := range ids {
		if strings.Contains(id, "/") {
			// Not a deck — deck storage and internal/rcache's resolve cache
			// share the same S3 bucket (tan-nakamura), and rcache namespaces
			// its keys as "<namespace>/<hash>" (e.g. "youtube/<hash>") to get
			// its own S3 "folder". Deck IDs are always flat (nanoid-style, no
			// "/"), so this is the cheap way to tell the two apart without a
			// deckstore-level prefix filter that both callers would need to
			// pass through. Skipping here (not attempting Get+unmarshal)
			// avoids a wasted S3 GET per cache entry and the resulting
			// "skipping corrupted deck" warning spam every sweep.
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					slog.Error("deck cleanup panicked", "id", id, "panic", r)
				}
			}()

			data, err := store.Get(ctx, id)
			if err != nil {
				// Already gone, or transient store error — either way there's
				// nothing to clean up for this id right now; next sweep retries.
				return
			}
			var d Deck
			if err := json.Unmarshal(data, &d); err != nil {
				slog.Warn("deck cleanup: skipping corrupted deck", "id", id, "err", err)
				return
			}
			if now.Before(d.ExpiresAt) {
				return
			}
			if err := store.Delete(ctx, id); err != nil {
				slog.Error("deck cleanup: delete failed", "id", id, "err", err)
				return
			}
			mu.Lock()
			count++
			mu.Unlock()
		}(id)
	}
	wg.Wait()
	return count, nil
}

// StartCleanupLoop runs CleanupExpired on interval until stop is closed. A
// single run's own deadline is capped independently of interval so one slow
// sweep can't overlap the next.
func StartCleanupLoop(store deckstore.Store, interval time.Duration, stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				deleted, err := CleanupExpired(ctx, store)
				cancel()
				if err != nil {
					slog.Error("deck cleanup sweep failed", "err", err)
				} else if deleted > 0 {
					slog.Info("deck cleanup sweep done", "deleted", deleted)
				}
			case <-stop:
				return
			}
		}
	}()
}
