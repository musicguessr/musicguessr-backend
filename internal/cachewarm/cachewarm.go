// Package cachewarm gently pre-warms the resolve cache (see
// internal/rcache) for an entire Hitster deck once one of its cards has
// actually been scanned, so the rest of that deck resolves instantly for
// whoever scans it next — instead of every card in a deck being a "cold"
// lookup the very first time it's individually seen.
//
// Deliberately not a general-purpose task queue: no persistence, no
// retries, no external broker (Celery-style systems solve durability and
// distributed-worker problems this doesn't have). If the process restarts
// mid-warm, the queue is simply gone — warming naturally re-triggers the
// next time someone scans a card from that deck. What this does take
// seriously is pacing: warming an entire deck (50-300 cards) as fast as
// possible would fan out hundreds of yt-dlp/MusicBrainz/etc. requests in a
// burst, which is exactly the kind of automated traffic pattern that gets
// an IP rate-limited or banned by a free third-party API. One job runs at
// a time, with a fixed gap between them, regardless of how many decks are
// queued.
package cachewarm

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// ResolveFunc resolves and caches everything for one Spotify track ID — the
// same pipeline /api/resolve itself uses (metadata fanout, YouTube search,
// Spotify catalog lookup), so a warmed card and a live-scanned card end up
// populating (or hitting) the exact same downstream caches. Cheap to call
// again on an already-warm card: every stage it touches checks its own
// cache first.
type ResolveFunc func(ctx context.Context, spotifyID string) error

// dropRetryAfter is how long a deck waits before it may be re-triggered
// when its jobs were dropped because the queue was full. Much shorter than
// rewarmAfter: nothing was actually warmed in that case, so holding the
// deck back for the full rewarm window would leave it cold until tomorrow
// purely because it arrived during a busy moment.
const dropRetryAfter = 10 * time.Minute

// Warmer runs a single-consumer, ticker-paced background queue.
type Warmer struct {
	resolve    ResolveFunc
	pacing     time.Duration
	jobTimeout time.Duration
	queue      chan string
	stop       chan struct{}
	stopOnce   sync.Once
	warmedMu   sync.Mutex
	// Maps deck ID to the earliest time it may be triggered again, rather
	// than when it was last warmed — a deck whose jobs were dropped needs a
	// much shorter cooldown than one that actually warmed (see dropRetryAfter).
	nextAllowed   map[string]time.Time
	rewarmAfter   time.Duration
	queueCapacity int
}

// New starts a Warmer. pacing is the minimum gap between two resolve calls;
// rewarmAfter is how long a deck ID is remembered so a second scan from the
// same deck shortly after doesn't re-enqueue it; queueCapacity bounds how
// many pending jobs can be buffered before TriggerDeck starts dropping the
// tail of a deck rather than blocking the HTTP request that called it.
func New(resolve ResolveFunc, pacing, rewarmAfter time.Duration, queueCapacity int) *Warmer {
	w := &Warmer{
		resolve:       resolve,
		pacing:        pacing,
		jobTimeout:    20 * time.Second,
		queue:         make(chan string, queueCapacity),
		stop:          make(chan struct{}),
		nextAllowed:   make(map[string]time.Time),
		rewarmAfter:   rewarmAfter,
		queueCapacity: queueCapacity,
	}
	go w.run()
	return w
}

// ShouldWarm reports whether TriggerDeck would currently do any work for
// this deck. Callers use it to avoid paying for the cards map when the
// answer is no — building one means scanning the resolver's entire
// card→track lookup (tens of thousands of entries) under its read lock, on
// what is otherwise a hot request path, only for TriggerDeck to discard it.
//
// Advisory only: TriggerDeck re-checks under the lock, so two concurrent
// requests that both see true still result in exactly one enqueue. The
// loser just built a map for nothing — the same cost this gate removes in
// the common case, not a new one.
func (w *Warmer) ShouldWarm(deckID string) bool {
	if deckID == "" {
		return false
	}
	w.warmedMu.Lock()
	defer w.warmedMu.Unlock()
	return !w.onCooldownLocked(deckID)
}

func (w *Warmer) onCooldownLocked(deckID string) bool {
	next, ok := w.nextAllowed[deckID]
	return ok && time.Now().Before(next)
}

// TriggerDeck enqueues every card in this deck for background warming.
// Cheap and safe to call on every /api/resolve request: a deck already
// triggered within rewarmAfter is a no-op, and an already-warm card is a
// near-instant cache hit when its job eventually runs. cards maps card
// number to Spotify track ID (see Resolver.CardsInDeck) — empty Spotify IDs
// are skipped. Prefer gating the call with ShouldWarm so the cards map is
// only built when it can actually be used.
func (w *Warmer) TriggerDeck(deckID string, cards map[string]string) {
	if deckID == "" || len(cards) == 0 {
		return
	}

	w.warmedMu.Lock()
	if w.onCooldownLocked(deckID) {
		w.warmedMu.Unlock()
		return
	}
	// Claim the deck up front so concurrent requests don't all enqueue it;
	// downgraded to dropRetryAfter below if the queue turns out to be full.
	w.nextAllowed[deckID] = time.Now().Add(w.rewarmAfter)
	w.warmedMu.Unlock()

	queued := 0
	for _, spotifyID := range cards {
		if spotifyID == "" {
			continue
		}
		select {
		case w.queue <- spotifyID:
			queued++
		default:
			// Queue full — drop the rest rather than block the request that
			// triggered this. Nothing meaningful was warmed, so shorten the
			// cooldown instead of holding this deck back for the full rewarm
			// window over what is usually a transient burst.
			w.warmedMu.Lock()
			w.nextAllowed[deckID] = time.Now().Add(dropRetryAfter)
			w.warmedMu.Unlock()
			slog.Warn("cachewarm: queue full, dropping remaining jobs", "deck_id", deckID, "queued", queued)
			return
		}
	}
	slog.Debug("cachewarm: deck queued for warming", "deck_id", deckID, "cards", queued)
}

// Stop halts the background worker. Safe to call multiple times.
func (w *Warmer) Stop() {
	w.stopOnce.Do(func() { close(w.stop) })
}

func (w *Warmer) run() {
	ticker := time.NewTicker(w.pacing)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			select {
			case spotifyID := <-w.queue:
				w.resolveOne(spotifyID)
			default:
				// Nothing queued this tick.
			}
		}
	}
}

func (w *Warmer) resolveOne(spotifyID string) {
	ctx, cancel := context.WithTimeout(context.Background(), w.jobTimeout)
	defer cancel()
	if err := w.resolve(ctx, spotifyID); err != nil {
		slog.Debug("cachewarm: warm failed", "spotify_id", spotifyID, "err", err)
	}
}
