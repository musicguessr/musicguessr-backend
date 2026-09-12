// Package rcache is a persistent, two-tier cache for the results of
// expensive per-scan lookups (YouTube search via yt-dlp, metadata provider
// fan-out) — the same Hitster card resolves to the same track forever, so
// there is no reason to repeat those lookups every time it's scanned again.
//
//   - Valkey/Redis (optional, "hot" tier): fast, TTL'd (default 30 days).
//   - S3-compatible storage (optional, "permanent" tier): authoritative,
//     never expires — a Valkey cache miss (cold start, eviction, TTL expiry,
//     Valkey not deployed at all) falls back to S3 and backfills Valkey.
//
// Either tier can be left unconfigured; a Cache with neither configured is a
// harmless no-op (every Get misses, every Set is dropped) so this is purely
// additive — existing deployments keep working unchanged until the relevant
// env vars are set.
package rcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/musicguessr/musicguessr-backend/internal/deckstore"
)

const defaultTTL = 30 * 24 * time.Hour

type Cache struct {
	valkey *valkeyClient
	s3     deckstore.Store
	ttl    time.Duration
}

// New builds a Cache from environment variables:
//
//	VALKEY_ADDR, VALKEY_PASSWORD, VALKEY_DB          — hot tier (optional)
//	RESOLVE_CACHE_PROVIDER=s3 + the usual RESOLVE_CACHE_* S3 vars
//	  (see deckstore.NewWithPrefix)                  — permanent tier (optional)
//	RESOLVE_CACHE_TTL_SECONDS                        — Valkey TTL, default 30 days
//
// Never returns an error for a missing/unreachable Valkey — that tier is
// simply skipped, since the S3 tier (or a no-op cache) is a safe fallback
// and the game must keep working without either.
func New() (*Cache, error) {
	c := &Cache{ttl: defaultTTL}

	if s := os.Getenv("RESOLVE_CACHE_TTL_SECONDS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			c.ttl = time.Duration(n) * time.Second
		}
	}

	if addr := os.Getenv("VALKEY_ADDR"); addr != "" {
		db := 0
		if s := os.Getenv("VALKEY_DB"); s != "" {
			if n, err := strconv.Atoi(s); err == nil {
				db = n
			}
		}
		c.valkey = newValkeyClient(addr, os.Getenv("VALKEY_PASSWORD"), db)
		slog.Info("rcache: valkey hot tier configured", "addr", addr)
	}

	s3Store, err := deckstore.NewWithPrefix("RESOLVE_CACHE", "")
	if err != nil {
		// A misconfigured (not merely absent) permanent tier is worth
		// surfacing at startup rather than silently falling back — but it
		// must not be fatal, since the cache is an optimization, not a
		// dependency the app needs to run.
		slog.Warn("rcache: permanent (S3) tier disabled due to config error", "err", err)
	} else if s3Store != nil {
		c.s3 = s3Store
		slog.Info("rcache: permanent S3 tier configured")
	}

	return c, nil
}

// Enabled reports whether at least one tier is configured.
func (c *Cache) Enabled() bool {
	return c != nil && (c.valkey != nil || c.s3 != nil)
}

// cacheKey derives a storage-safe key from a namespace (e.g. "youtube",
// "metadata", "spotify-track") and an arbitrary caller-supplied string
// (artist/title, a Spotify track ID, ...) which may contain spaces, slashes
// or unicode — none of which are safe to use directly as a Valkey key or an
// S3 object path segment — so it's hashed. "/" separates namespace from
// hash so each namespace lands in its own S3 "folder" (a plain key prefix —
// S3 has no real directories, but consoles/tools group by it) instead of
// every entry from every namespace sitting flat in one bucket. deckstore's
// local backend (only reachable here if RESOLVE_CACHE_PROVIDER=local is set
// explicitly — the default is S3-only or disabled) creates the namespace
// subdirectory on demand, so this is safe there too.
func cacheKey(namespace, key string) string {
	sum := sha256.Sum256([]byte(key))
	return namespace + "/" + hex.EncodeToString(sum[:])
}

// GetJSON looks up namespace/key, trying Valkey first, then S3 (backfilling
// Valkey on an S3 hit). Returns ok=false on a clean miss in both tiers, or
// if neither tier is configured.
func (c *Cache) GetJSON(ctx context.Context, namespace, key string, out any) (ok bool) {
	if c == nil {
		return false
	}
	sk := cacheKey(namespace, key)

	if c.valkey != nil {
		data, found, err := c.valkey.Get(ctx, sk)
		if err != nil {
			slog.Debug("rcache: valkey get failed", "namespace", namespace, "err", err)
		} else if found {
			if err := json.Unmarshal(data, out); err == nil {
				return true
			}
			slog.Warn("rcache: valkey value decode failed, ignoring", "namespace", namespace)
		}
	}

	if c.s3 != nil {
		data, err := c.s3.Get(ctx, sk)
		if err != nil {
			if !errors.Is(err, deckstore.ErrNotFound) {
				slog.Debug("rcache: s3 get failed", "namespace", namespace, "err", err)
			}
			return false
		}
		if err := json.Unmarshal(data, out); err != nil {
			slog.Warn("rcache: s3 value decode failed, ignoring", "namespace", namespace, "err", err)
			return false
		}
		if c.valkey != nil {
			if err := c.valkey.Set(ctx, sk, data, c.ttl); err != nil {
				slog.Debug("rcache: valkey backfill failed", "namespace", namespace, "err", err)
			}
		}
		return true
	}

	return false
}

// SetJSON stores v under namespace/key in every configured tier: Valkey with
// the configured TTL, S3 permanently. Failures are logged, not returned —
// caching is best-effort and must never fail the request it's attached to.
func (c *Cache) SetJSON(ctx context.Context, namespace, key string, v any) {
	if c == nil || (c.valkey == nil && c.s3 == nil) {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		slog.Warn("rcache: encode failed", "namespace", namespace, "err", err)
		return
	}
	sk := cacheKey(namespace, key)

	if c.valkey != nil {
		if err := c.valkey.Set(ctx, sk, data, c.ttl); err != nil {
			slog.Debug("rcache: valkey set failed", "namespace", namespace, "err", err)
		}
	}
	if c.s3 != nil {
		if err := c.s3.Put(ctx, sk, data); err != nil {
			slog.Debug("rcache: s3 put failed", "namespace", namespace, "err", err)
		}
	}
}
