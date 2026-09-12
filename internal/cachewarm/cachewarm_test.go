package cachewarm

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTriggerDeck_ResolvesEachCard(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	done := make(chan struct{})

	resolve := func(_ context.Context, spotifyID string) error {
		mu.Lock()
		seen[spotifyID] = true
		n := len(seen)
		mu.Unlock()
		if n == 3 {
			close(done)
		}
		return nil
	}

	w := New(resolve, 5*time.Millisecond, time.Hour, 100)
	t.Cleanup(w.Stop)

	w.TriggerDeck("testdeck", map[string]string{
		"00001": "sp1",
		"00002": "sp2",
		"00003": "sp3",
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for all cards to be warmed")
	}

	mu.Lock()
	defer mu.Unlock()
	for _, id := range []string{"sp1", "sp2", "sp3"} {
		if !seen[id] {
			t.Errorf("expected %q to have been resolved", id)
		}
	}
}

func TestTriggerDeck_SkipsEmptySpotifyIDs(t *testing.T) {
	var calls int32
	resolve := func(_ context.Context, _ string) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}

	w := New(resolve, 5*time.Millisecond, time.Hour, 100)
	t.Cleanup(w.Stop)

	w.TriggerDeck("testdeck", map[string]string{
		"00001": "sp1",
		"00002": "", // unresolved card in the source database — must be skipped
	})

	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("got %d resolve calls, want 1 (empty spotify ID skipped)", got)
	}
}

func TestTriggerDeck_DedupesWithinRewarmWindow(t *testing.T) {
	var calls int32
	resolve := func(_ context.Context, _ string) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}

	w := New(resolve, 5*time.Millisecond, time.Hour, 100)
	t.Cleanup(w.Stop)

	cards := map[string]string{"00001": "sp1"}
	w.TriggerDeck("testdeck", cards)
	w.TriggerDeck("testdeck", cards) // second scan from the same deck shortly after
	w.TriggerDeck("testdeck", cards)

	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("got %d resolve calls, want 1 — repeat triggers within rewarmAfter should be deduped", got)
	}
}

func TestTriggerDeck_ReTriggersAfterRewarmWindowElapses(t *testing.T) {
	var calls int32
	resolve := func(_ context.Context, _ string) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}

	w := New(resolve, 5*time.Millisecond, 20*time.Millisecond, 100)
	t.Cleanup(w.Stop)

	cards := map[string]string{"00001": "sp1"}
	w.TriggerDeck("testdeck", cards)
	time.Sleep(40 * time.Millisecond) // let the rewarm window elapse
	w.TriggerDeck("testdeck", cards)

	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("got %d resolve calls, want 2 — a deck should re-warm once rewarmAfter has passed", got)
	}
}

func TestTriggerDeck_EmptyDeckOrCardsIsNoop(t *testing.T) {
	var calls int32
	resolve := func(_ context.Context, _ string) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}

	w := New(resolve, 5*time.Millisecond, time.Hour, 100)
	t.Cleanup(w.Stop)

	w.TriggerDeck("", map[string]string{"00001": "sp1"})
	w.TriggerDeck("testdeck", nil)

	time.Sleep(30 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("got %d resolve calls, want 0", got)
	}
}

func TestTriggerDeck_QueueFullDropsRatherThanBlocks(t *testing.T) {
	block := make(chan struct{})
	resolve := func(_ context.Context, _ string) error {
		<-block // never returns until the test releases it
		return nil
	}

	w := New(resolve, time.Millisecond, time.Hour, 1) // capacity 1
	t.Cleanup(func() {
		close(block)
		w.Stop()
	})

	cards := map[string]string{
		"00001": "sp1",
		"00002": "sp2",
		"00003": "sp3",
	}

	done := make(chan struct{})
	go func() {
		w.TriggerDeck("testdeck", cards)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("TriggerDeck blocked instead of dropping jobs once the queue was full")
	}
}

func TestStop_IsSafeToCallMultipleTimes(t *testing.T) {
	w := New(func(context.Context, string) error { return nil }, time.Millisecond, time.Hour, 10)
	w.Stop()
	w.Stop()
}
