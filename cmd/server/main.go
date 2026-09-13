package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/musicguessr/musicguessr-backend/internal/axiomlog"
	"github.com/musicguessr/musicguessr-backend/internal/cachewarm"
	"github.com/musicguessr/musicguessr-backend/internal/deck"
	"github.com/musicguessr/musicguessr-backend/internal/deckstore"
	"github.com/musicguessr/musicguessr-backend/internal/itunes"
	"github.com/musicguessr/musicguessr-backend/internal/metadata"
	"github.com/musicguessr/musicguessr-backend/internal/ratelimit"
	"github.com/musicguessr/musicguessr-backend/internal/rcache"
	"github.com/musicguessr/musicguessr-backend/internal/requestid"
	"github.com/musicguessr/musicguessr-backend/internal/resolver"
	"github.com/musicguessr/musicguessr-backend/internal/spotifyapi"
	"github.com/musicguessr/musicguessr-backend/internal/youtube"
)

// Set via -ldflags "-X main.gitCommit=... -X main.buildDate=..." at image
// build time (see Dockerfile's GIT_COMMIT/BUILD_DATE build args and
// docker-multiarch.yml, which passes github.sha and the current UTC date).
// A local `go run`/`go build` with no ldflags leaves these at their zero
// values, which is expected — only released container images set them.
var (
	gitCommit = "unknown"
	buildDate = "unknown"
)

// metadataCacheAdapter implements metadata.Cache on top of the persistent,
// two-tier rcache.Cache (Valkey + S3) — see internal/rcache. The per-call ttl
// metadata.Cache.Set receives is intentionally ignored: rcache's own
// RESOLVE_CACHE_TTL_SECONDS is the single source of truth for how long the
// Valkey tier holds an entry, so every cached lookup in the app shares one
// policy instead of each package's default TTL silently taking effect here.
type metadataCacheAdapter struct{ c *rcache.Cache }

func (a metadataCacheAdapter) Get(key string) (*itunes.Track, bool) {
	var t itunes.Track
	if a.c.GetJSON(context.Background(), "metadata", key, &t) {
		return &t, true
	}
	return nil, false
}

func (a metadataCacheAdapter) Set(key string, t *itunes.Track, _ time.Duration) {
	a.c.SetJSON(context.Background(), "metadata", key, t)
}

// youtubeCacheAdapter implements youtube.Cache the same way.
type youtubeCacheAdapter struct{ c *rcache.Cache }

func (a youtubeCacheAdapter) Get(key string) (string, bool) {
	var videoID string
	if a.c.GetJSON(context.Background(), "youtube", key, &videoID) {
		return videoID, true
	}
	return "", false
}

func (a youtubeCacheAdapter) Set(key, videoID string) {
	a.c.SetJSON(context.Background(), "youtube", key, videoID)
}

// spotifyCacheAdapter implements spotifyapi.Cache the same way — keyed by
// Spotify track ID (not artist|title, since this lookup is an exact-ID
// fetch), so it shares the "metadata"/"youtube" namespaces' persistence
// without colliding with either.
type spotifyCacheAdapter struct{ c *rcache.Cache }

func (a spotifyCacheAdapter) Get(key string) (*spotifyapi.Track, bool) {
	var t spotifyapi.Track
	if a.c.GetJSON(context.Background(), "spotify-track", key, &t) {
		return &t, true
	}
	return nil, false
}

func (a spotifyCacheAdapter) Set(key string, t *spotifyapi.Track) {
	a.c.SetJSON(context.Background(), "spotify-track", key, t)
}

// sharedTransport is reused by all HTTP clients for connection pooling.
var sharedTransport = &http.Transport{
	MaxIdleConns:        100,
	MaxIdleConnsPerHost: 10,
	IdleConnTimeout:     30 * time.Second,
}

// Spotify oEmbed cache — avoids re-fetching the same track HTML on every scan.
type spotifyCacheEntry struct {
	artist, title string
	expires       time.Time
}

var (
	spotifyCacheMu sync.RWMutex
	spotifyCache   = map[string]spotifyCacheEntry{}
)

const spotifyCacheTTL = 7 * 24 * time.Hour

// startSpotifyCacheCleanup periodically drops expired entries from the
// in-process oEmbed cache. Mirrors ratelimit.Limiter.StartCleanup: entries
// carry an expiry that the read path honours, but nothing reclaimed the
// memory of one that's simply never looked up again.
func startSpotifyCacheCleanup(interval time.Duration, stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				now := time.Now()
				spotifyCacheMu.Lock()
				for id, e := range spotifyCache {
					if now.After(e.expires) {
						delete(spotifyCache, id)
					}
				}
				spotifyCacheMu.Unlock()
			case <-stop:
				return
			}
		}
	}()
}

// bodyAllowedForStatus mirrors net/http's own (unexported) rule for which
// status codes may carry a response body. Statuses that may not (1xx, 204,
// 304) make any write — including gzip's header/trailer bytes — fail with
// "http: request method or response status code does not allow body".
func bodyAllowedForStatus(status int) bool {
	switch {
	case status >= 100 && status <= 199:
		return false
	case status == http.StatusNoContent:
		return false
	case status == http.StatusNotModified:
		return false
	}
	return true
}

// gzipResponseWriter wraps http.ResponseWriter to compress the body.
type gzipResponseWriter struct {
	http.ResponseWriter
	gz *gzip.Writer
	// Defaults to true so a handler that writes without ever calling
	// WriteHeader (implicit 200) still gets its body flushed — only an
	// explicit bodiless status flips this off.
	bodyAllowed bool
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) { return g.gz.Write(b) }

// WriteHeader drops the Content-Encoding promise for responses that can't
// carry a body. Without this, /api/client-error's 204 (and any 304) still
// advertised gzip and then had gz.Close()'s trailer write rejected by
// net/http, logging a spurious ERROR for every single such response — which
// made the ERROR level pure noise, since that was the only thing producing
// one. Content-Encoding is deleted rather than left dangling because a 204
// that claims a gzip body it doesn't have is also just wrong on the wire.
// Vary stays: the response still varies by Accept-Encoding.
func (g *gzipResponseWriter) WriteHeader(status int) {
	if !bodyAllowedForStatus(status) {
		g.bodyAllowed = false
		g.ResponseWriter.Header().Del("Content-Encoding")
	}
	g.ResponseWriter.WriteHeader(status)
}

func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		gz := gzip.NewWriter(w)
		gw := &gzipResponseWriter{ResponseWriter: w, gz: gz, bodyAllowed: true}
		defer func() {
			if !gw.bodyAllowed {
				// Nothing was (or could be) written through gz — closing it
				// here would emit the gzip header/trailer into a response
				// that's not allowed to have one.
				return
			}
			if err := gz.Close(); err != nil {
				slog.Error("gzip close failed", "err", err)
			}
		}()
		next.ServeHTTP(gw, r)
	})
}

type resolveResponse struct {
	SpotifyID      string            `json:"spotify_id"`
	SpotifyURL     string            `json:"spotify_url"`
	Artist         string            `json:"artist,omitempty"`
	Title          string            `json:"title,omitempty"`
	Year           int               `json:"year,omitempty"`
	ArtworkURL     string            `json:"artwork_url,omitempty"`
	Album          string            `json:"album,omitempty"`
	Explicit       bool              `json:"explicit,omitempty"`
	YouTubeVideoID string            `json:"youtube_video_id,omitempty"`
	Links          map[string]string `json:"links"`
	// RequestID lets a user hand over one short string in a bug report that
	// a maintainer can grep straight to this request's server-side logs —
	// see internal/requestid.
	RequestID string `json:"request_id,omitempty"`
}

type errResponse struct {
	Error string `json:"error"`
}

// clientIP extracts the caller's real address. Production always sits
// behind Cloudflare Tunnel — cloudflared is the only thing that can reach
// this container on musicguessr.network, so CF-Connecting-IP (set by
// Cloudflare's edge, not forgeable by the end client) is trustworthy here.
// X-Forwarded-For is a fallback for local/non-Cloudflare deployments; the
// RemoteAddr fallback below covers direct connections (e.g. `go run` in dev).
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if ip := strings.TrimSpace(strings.Split(xff, ",")[0]); ip != "" {
			return ip
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

type clientErrorRequest struct {
	Message   string `json:"message"`
	Stack     string `json:"stack,omitempty"`
	URL       string `json:"url,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
	// Free-form tag identifying which client-side code path reported this
	// (e.g. "global-error-handler", "scanner-timeout") — lets log queries
	// filter by source without parsing message text.
	Context string `json:"context,omitempty"`
	// RequestID, when the report relates to a specific earlier /api/resolve
	// call (e.g. a YouTube-blocked report for the card that call resolved),
	// ties this report back to that request's own "resolve request" log
	// line — see internal/requestid.
	RequestID string `json:"request_id,omitempty"`
}

// clientErrorFieldLimit caps each field's logged length — the request body
// itself is already bounded by MaxBytesReader below, but a single
// pathologically long field (a minified stack trace, say) shouldn't be
// allowed to dominate a log line on its own.
const clientErrorFieldLimit = 2000

func truncateField(s string) string {
	if len(s) > clientErrorFieldLimit {
		return s[:clientErrorFieldLimit] + "…(truncated)"
	}
	return s
}

// handleClientError logs a best-effort report of a client-only failure —
// see the /api/client-error registration above for why this exists. It
// never fails loudly: a malformed body just gets a 204, since the frontend
// sends these fire-and-forget and has nothing useful to do with an error
// response for its own error-reporting call.
func handleClientError(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10) // 8 KB

	var req clientErrorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.Message != "" {
		slog.Warn("client error report",
			"context", truncateField(req.Context),
			"message", truncateField(req.Message),
			"stack", truncateField(req.Stack),
			"url", truncateField(req.URL),
			"user_agent", truncateField(req.UserAgent),
			"ip", clientIP(r),
			"resolve_request_id", truncateField(req.RequestID),
			"request_id", requestid.FromContext(r.Context()),
			"session_id", truncateField(r.Header.Get("X-Session-Id")),
		)
	}
	w.WriteHeader(http.StatusNoContent)
}

// rateLimited wraps a handler with a per-IP token-bucket check. Applied
// selectively to the expensive/abusable endpoints (metadata+yt-dlp fan-out,
// deck creation/import) rather than globally — /health and static asset
// serving elsewhere have no such cost to protect.
func rateLimited(limiter *ratelimit.Limiter, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow(clientIP(r)) {
			w.Header().Set("Retry-After", "5")
			writeJSON(w, http.StatusTooManyRequests, errResponse{"too many requests, please slow down"})
			return
		}
		next(w, r)
	}
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Session-Id")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("writeJSON encode failed", "err", err)
	}
}

func main() {
	level := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}

	// Optional external log shipping (see internal/axiomlog) — nil when
	// AXIOM_TOKEN/AXIOM_DATASET aren't set, in which case logging is
	// unchanged from before (stderr only, picked up by journalctl on the
	// Pi). JSON rather than the previous plain-text format so a shipped
	// line and a locally-grepped line are the exact same structured record.
	axiomWriter := axiomlog.New()
	logDest := io.Writer(os.Stderr)
	if axiomWriter != nil {
		logDest = io.MultiWriter(os.Stderr, axiomWriter)
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(logDest, &slog.HandlerOptions{Level: level})))
	if axiomWriter != nil {
		slog.Info("axiom log shipping enabled")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	res := resolver.New()
	var httpClient = &http.Client{Timeout: 8 * time.Second, Transport: sharedTransport}

	store, err := deckstore.New()
	if err != nil {
		slog.Error("deckstore init failed", "err", err)
		os.Exit(1)
	}
	deckHandler := deck.NewHandler(store)

	// Persistent resolve cache (Valkey hot tier + S3 permanent tier, both
	// optional — see internal/rcache) so repeat scans of the same card skip
	// the metadata-provider fan-out and the yt-dlp search entirely instead of
	// only benefiting from the in-process, restart-losing caches those
	// packages default to.
	resolveCache, err := rcache.New()
	if err != nil {
		slog.Error("rcache init failed", "err", err)
		os.Exit(1)
	}
	// Optional: authoritative per-track metadata straight from Spotify's own
	// catalog (Client Credentials Flow — app-only, no user auth) for the
	// exact track ID already resolved from the QR code. nil when
	// SPOTIFY_CLIENT_ID/SPOTIFY_CLIENT_SECRET aren't set; every call site
	// treats a nil *Client the same as "unavailable right now" (see
	// spotifyapi's circuit breaker), so this is safe to leave unconfigured.
	spotifyClient := spotifyapi.New()
	if spotifyClient != nil {
		slog.Info("spotify metadata enrichment enabled")
	}

	if resolveCache.Enabled() {
		metadata.SetCache(metadataCacheAdapter{c: resolveCache})
		youtube.SetCache(youtubeCacheAdapter{c: resolveCache})
		// Without this, a Spotify track lookup only survives in
		// spotifyapi's own in-process map — lost on every restart (which
		// happens often across deploys), so the very next scan of an
		// already-seen card would hit Spotify's API again for no reason.
		// Sharing the same persistent Valkey+S3 tiers as metadata/youtube
		// means it's also the permanent, restart-proof source of truth.
		spotifyapi.SetCache(spotifyCacheAdapter{c: resolveCache})
	}

	// Background deck-cache warmer (see internal/cachewarm) — runs the same
	// metadata/YouTube/Spotify pipeline as a live resolve, just paced slowly
	// and triggered for a whole deck once any one of its cards is scanned.
	warmResolve := func(ctx context.Context, spotifyID string) error {
		artist, title := fetchSpotifyMeta(ctx, httpClient, spotifyID)
		if artist == "" && title == "" {
			return errors.New("no spotify metadata for track")
		}
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = youtube.SearchVideoID(ctx, artist, title, true)
		}()
		_, _ = metadata.Resolve(ctx, artist, title)
		wg.Wait()
		if spotifyClient != nil {
			_, _ = spotifyClient.GetTrack(ctx, spotifyID)
		}
		return nil
	}
	// ~1 card every 6s keeps this well under any provider's own rate limits
	// even for a large deck; 24h dedup means a deck already warmed today
	// won't re-trigger on every subsequent scan from it.
	warmer := cachewarm.New(warmResolve, 6*time.Second, 24*time.Hour, 2000)

	// Both endpoints groups are public with no auth, so they're the surface
	// most exposed to abuse (scripted scraping, or someone hammering the
	// yt-dlp subprocess path). Limits are per-IP and generous enough for
	// normal gameplay/deck creation, not for scripted load. Deck creation is
	// stricter since it fans out metadata enrichment across up to 300 cards
	// per call.
	resolveLimiter := ratelimit.New(0.5, 15, 10*time.Minute) // ~30/min sustained, burst 15
	deckLimiter := ratelimit.New(0.1, 3, 10*time.Minute)     // ~6/min sustained, burst 3
	// Client-error reports are small and infrequent by nature (a handful per
	// real session at most) — this exists to catch someone trying to use the
	// endpoint as a free-form log-spam sink, not to throttle real usage.
	clientErrorLimiter := ratelimit.New(0.2, 5, 10*time.Minute) // ~12/min sustained, burst 5
	// Deck reads are cheap per call but still a billed S3 GET each, including
	// for ids that don't exist — without a limit this is the only public
	// endpoint doing external I/O that someone can hammer for free. Generous
	// enough that opening a deck, reloading it, and paging through a shared
	// link never comes close.
	deckReadLimiter := ratelimit.New(2, 30, 10*time.Minute) // ~120/min sustained, burst 30
	stopCleanup := make(chan struct{})
	resolveLimiter.StartCleanup(5*time.Minute, stopCleanup)
	deckLimiter.StartCleanup(5*time.Minute, stopCleanup)
	clientErrorLimiter.StartCleanup(5*time.Minute, stopCleanup)
	deckReadLimiter.StartCleanup(5*time.Minute, stopCleanup)
	// The in-process Spotify oEmbed cache has per-entry expiry but nothing
	// that ever removes a stale entry — now that cachewarm pushes whole decks
	// through fetchSpotifyMeta, it would otherwise grow monotonically toward
	// one entry per card in the entire Hitster catalogue and never shrink.
	startSpotifyCacheCleanup(30*time.Minute, stopCleanup)

	// Expired decks are never deleted otherwise (deckstore has no TTL of its
	// own) — an hourly sweep is frequent enough that storage never grows far
	// past what's actually live, without hammering the store's List/Get on a
	// hobby-scale deck count.
	deck.StartCleanupLoop(store, 1*time.Hour, stopCleanup)

	mux := http.NewServeMux()

	mux.HandleFunc("/api/deck/validate-yt", rateLimited(deckLimiter, deck.ValidateYtHandler))
	mux.HandleFunc("/api/deck/import-playlist", rateLimited(deckLimiter, deck.ImportPlaylistHandler))
	mux.HandleFunc("/api/deck/", rateLimited(deckReadLimiter, deckHandler.GetDeck))
	mux.HandleFunc("/api/deck", rateLimited(deckLimiter, deckHandler.CreateDeck))

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status":     "ok",
			"commit":     gitCommit,
			"build_date": buildDate,
		})
	})

	// A no-cost stand-in for a real error-tracking service (Sentry etc.):
	// the frontend has no way to surface client-only failures (e.g. QR
	// detection silently failing on a hardened/privacy browser — nothing
	// ever reaches /api/resolve in that case) anywhere we can see, so this
	// gives it somewhere to report them to. Logged only, no storage/alerting
	// beyond that — deliberately minimal.
	mux.HandleFunc("/api/client-error", rateLimited(clientErrorLimiter, handleClientError))

	mux.HandleFunc("/api/resolve", rateLimited(resolveLimiter, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		qrURL := r.URL.Query().Get("url")
		if qrURL == "" {
			writeJSON(w, http.StatusBadRequest, errResponse{"missing url parameter"})
			return
		}
		reqID := requestid.FromContext(r.Context())
		// Client-generated, in-memory-only for the lifetime of one browser
		// tab (see SessionIdService on the frontend) — lets logs correlate
		// multiple actions within a single visit (e.g. a scanner timeout
		// followed by this resolve) without the backend ever persisting an
		// identifier across visits, which would be tracking in the sense
		// this app's FAQ explicitly promises it doesn't do.
		sessionID := truncateField(r.Header.Get("X-Session-Id"))

		// Spotify fetch (8s) + metadata.Resolve (6s) + youtube.SearchVideoID
		// (up to two sequential yt-dlp passes, 20s each when uncapped) can
		// otherwise sum to nearly a minute with nothing bounding the request
		// as a whole; this deadline caps the total regardless, at the cost of
		// truncating whichever stage is still running when it fires.
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		r = r.WithContext(ctx)

		spotifyID, deckID, err := res.Resolve(qrURL)
		if err != nil {
			slog.Warn("resolve: card not found", "request_id", reqID, "session_id", sessionID, "url", qrURL, "err", err)
			writeJSON(w, http.StatusNotFound, errResponse{err.Error()})
			return
		}
		// Best-effort: warm the rest of this deck's cache in the background
		// now that we know someone's actively playing it — see
		// internal/cachewarm. Paced slowly and deduped per-deck, so this
		// never adds request volume anywhere close to what would risk a
		// rate limit/ban from a provider. ShouldWarm gates CardsInDeck
		// because that call scans the resolver's whole lookup map under its
		// read lock — far too expensive to pay on every request just to have
		// TriggerDeck drop it as already-warmed, which is the common case.
		if warmer.ShouldWarm(deckID) {
			warmer.TriggerDeck(deckID, res.CardsInDeck(deckID))
		}

		resp := resolveResponse{
			SpotifyID:  spotifyID,
			SpotifyURL: resolver.SpotifyURL(spotifyID),
			Links:      make(map[string]string),
			RequestID:  reqID,
		}

		// Enrich: Spotify oEmbed → artist/title
		artist, title := fetchSpotifyMeta(r.Context(), httpClient, spotifyID)
		if artist != "" {
			// The yt-dlp search and the metadata provider fan-out are
			// independent — both only need Spotify's artist/title, neither
			// depends on the other's result — so they used to run one after
			// the other for no reason, making a cold (uncached) card's total
			// wait the *sum* of both (metadata: up to 6s; yt-dlp: up to 20s).
			// Running them concurrently caps it at whichever is slower
			// instead, which is the actual bottleneck users were hitting on
			// first-time scans. youtube.SearchVideoID always searches with
			// Spotify's raw artist/title now (previously upgraded to
			// metadata's cleaned title when available) — its own coreTitle/
			// normalizeForQuery already strip the kind of noise
			// ("(Radio Edit)", parenthetical suffixes, diacritics) that
			// metadata's cleanup mainly helped with, so this isn't expected
			// to cost meaningful match quality.
			allowVariants := r.URL.Query().Get("yt_variants") == "1"
			var wg sync.WaitGroup
			var ytVideoID string
			var ytErr error
			wg.Add(1)
			go func() {
				defer wg.Done()
				ytVideoID, ytErr = youtube.SearchVideoID(r.Context(), artist, title, allowVariants)
			}()

			// spotifyClient is nil when SPOTIFY_CLIENT_ID/SPOTIFY_CLIENT_SECRET
			// aren't configured — GetTrack on a nil *Client returns an error
			// immediately, so this goroutine is cheap and harmless either way.
			var spTrack *spotifyapi.Track
			wg.Add(1)
			go func() {
				defer wg.Done()
				t, err := spotifyClient.GetTrack(r.Context(), spotifyID)
				if err != nil {
					slog.Debug("spotify track lookup unavailable, using metadata fanout instead", "spotify_id", spotifyID, "err", err)
					return
				}
				spTrack = t
			}()

			if track, err := metadata.Resolve(r.Context(), artist, title); err == nil {
				resp.Artist = track.Artist
				resp.Title = track.Title
				resp.Year = track.Year
				resp.ArtworkURL = track.ArtworkURL
				// StreamingLinks() only ever returns youtube_music/youtube/tidal/deezer —
				// it must not clobber the apple_music entry set above.
				resp.Links = resolver.StreamingLinks(track.Artist, track.Title)
				if track.AppleMusicURL != "" {
					resp.Links["apple_music"] = track.AppleMusicURL
				}
			} else {
				slog.Warn("metadata resolve failed", "artist", artist, "title", title, "err", err)
				resp.Artist = artist
				resp.Title = title
				resp.Links = resolver.StreamingLinks(artist, title)
			}

			wg.Wait()

			// Spotify's own catalog data for this exact track ID is
			// authoritative — no fuzzy artist/title matching involved — so
			// when available it overrides the metadata fanout's
			// majority-voted fields, which can occasionally land on a
			// re-recording/cover (see chooseMostCommonInt's tiebreak
			// comment). Apple Music URL and the other streaming links still
			// come from the fanout above; Spotify's API doesn't provide them.
			if spTrack != nil {
				if spTrack.Artist != "" {
					resp.Artist = spTrack.Artist
				}
				if spTrack.Title != "" {
					resp.Title = spTrack.Title
				}
				if spTrack.Year != 0 {
					resp.Year = spTrack.Year
				}
				if spTrack.ArtworkURL != "" {
					resp.ArtworkURL = spTrack.ArtworkURL
				}
				// Album/Explicit have no fanout equivalent to fall back to —
				// only Spotify's own API provides them.
				resp.Album = spTrack.Album
				resp.Explicit = spTrack.Explicit
			}

			if ytErr == nil {
				resp.YouTubeVideoID = ytVideoID
			} else {
				slog.Warn("youtube search failed", "artist", artist, "title", title, "err", ytErr, "request_id", reqID)
			}
		}
		resp.Links["spotify"] = resp.SpotifyURL

		// Anchor log line for this request — a user reporting request_id
		// alongside a bug gets a maintainer straight to exactly what this
		// request resolved to (nearby WARN lines above, if any, then explain
		// why), rather than having to correlate by approximate timestamp.
		slog.Info("resolve request", "request_id", reqID, "session_id", sessionID, "spotify_id", spotifyID, "artist", resp.Artist, "title", resp.Title, "year", resp.Year)

		writeJSON(w, http.StatusOK, resp)
	}))

	// cors must wrap gzip, not the other way round — otherwise an OPTIONS
	// preflight's empty 204 response (written by cors, never reaching gzip's
	// body writer) still gets Content-Encoding: gzip/Vary headers set by
	// gzipMiddleware before cors ever runs, and gz.Close()'s trailer write
	// fails against the already-204'd ResponseWriter. requestid wraps
	// everything else so the ID exists (and its response header is set)
	// before any of them run, including on the OPTIONS/CORS-only path.
	handler := requestid.Middleware(cors(gzipMiddleware(mux)))
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("musicguessr backend starting", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-stop
	slog.Info("shutdown signal received, shutting down server")
	close(stopCleanup)
	warmer.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownErr := srv.Shutdown(ctx)
	if shutdownErr != nil {
		slog.Error("graceful shutdown failed", "err", shutdownErr)
	}
	if axiomWriter != nil {
		// Flushes whatever's still buffered (e.g. this shutdown sequence's
		// own log lines) before the process actually exits. Must run before
		// any os.Exit below — exiting first would discard exactly the
		// shutdown-failure line a maintainer would want shipped.
		axiomWriter.Close()
	}
	if shutdownErr != nil {
		os.Exit(1)
	}
}

func fetchSpotifyMeta(ctx context.Context, client *http.Client, trackID string) (artist, title string) {
	spotifyCacheMu.RLock()
	if e, ok := spotifyCache[trackID]; ok && time.Now().Before(e.expires) {
		spotifyCacheMu.RUnlock()
		return e.artist, e.title
	}
	spotifyCacheMu.RUnlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://open.spotify.com/track/"+trackID, nil)
	if err != nil {
		slog.Error("spotify request creation failed", "trackID", trackID, "err", err)
		return "", ""
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("spotify page fetch failed", "trackID", trackID, "err", err)
		return "", ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		slog.Error("spotify page non-200", "trackID", trackID, "status", resp.StatusCode)
		return "", ""
	}

	// read up to first 32KB of the response
	limited := io.LimitReader(resp.Body, 32*1024)
	body, _ := io.ReadAll(limited)
	// Deliberately not named "html" — that would shadow the imported html
	// package for the rest of this function.
	page := string(body)

	// og:title → track title. Spotify uses several formats:
	//   "Track Name - song by Artist | Spotify"          (most common)
	//   "Track Name - song and lyrics by Artist | Spotify"
	//   "Track Name | Spotify"
	//   "Track Name - Radio edit"                        (no Spotify suffix — use raw)
	const ogTitleNeedle = `og:title" content="`
	if idx := strings.Index(page, ogTitleNeedle); idx != -1 {
		start := idx + len(ogTitleNeedle)
		if end := strings.Index(page[start:], `"`); end != -1 {
			raw := decodeHTMLEntities(page[start : start+end])
			slog.Debug("spotify og:title", "trackID", trackID, "raw", raw)
			if sep := strings.Index(raw, " - song"); sep != -1 {
				title = strings.TrimSpace(raw[:sep])
			} else if sep := strings.Index(raw, " | Spotify"); sep != -1 {
				candidate := raw[:sep]
				if by := strings.LastIndex(candidate, " by "); by != -1 {
					title = strings.TrimSpace(candidate[:by])
				} else {
					title = strings.TrimSpace(candidate)
				}
			} else {
				// No standard suffix — og:title IS the track name (e.g. "Loca Bambina - Radio edit")
				title = strings.TrimSpace(raw)
			}
		}
	}

	// og:description → artist from parts[0]; parts[1] as title fallback.
	// Format: "Artist · Title · Song · Year"
	// NOTE: for compilation albums Spotify puts the album name in parts[1], not the track title.
	// We therefore prefer og:title for the title and only fall back to og:description parts[1]
	// when og:title parsing returned nothing.
	const ogDescNeedle = `og:description" content="`
	if idx := strings.Index(page, ogDescNeedle); idx != -1 {
		start := idx + len(ogDescNeedle)
		if end := strings.Index(page[start:], `"`); end != -1 {
			desc := decodeHTMLEntities(page[start : start+end])
			slog.Debug("spotify og:description", "trackID", trackID, "desc", desc)
			parts := strings.Split(desc, " · ")
			if len(parts) >= 1 {
				artist = strings.TrimSpace(parts[0])
			}
			if title == "" && len(parts) >= 2 {
				title = strings.TrimSpace(parts[1])
				slog.Debug("spotify title from og:description fallback", "trackID", trackID, "title", title)
			}
		}
	}

	slog.Debug("spotify meta resolved", "trackID", trackID, "artist", artist, "title", title)
	if artist != "" || title != "" {
		spotifyCacheMu.Lock()
		spotifyCache[trackID] = spotifyCacheEntry{artist: artist, title: title, expires: time.Now().Add(spotifyCacheTTL)}
		spotifyCacheMu.Unlock()
	}
	return
}

// decodeHTMLEntities replaces HTML entities with their UTF-8 equivalents.
func decodeHTMLEntities(s string) string {
	return html.UnescapeString(s)
}
