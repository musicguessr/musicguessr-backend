// Package spotifyapi is a minimal Client Credentials Flow client for
// Spotify's Web API — app-only access to public catalog data (no user
// authorization, no redirect). It is entirely optional: when
// SPOTIFY_CLIENT_ID/SPOTIFY_CLIENT_SECRET aren't both set, New returns nil
// and every caller treats a nil *Client as "skip this enrichment", not an
// error. The credentials that back this are expected to occasionally go
// stale (rotated, revoked, or simply expired without anyone updating the
// deployed env file) — the circuit breaker in this package exists
// specifically so that failure mode degrades to "acts as if unconfigured"
// instead of adding a failing round-trip's latency to every request.
package spotifyapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

var httpClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        20,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     30 * time.Second,
	},
}

// Overridable so tests can point at an httptest server instead of the real
// Spotify API.
var (
	tokenURL   = "https://accounts.spotify.com/api/token"
	apiBaseURL = "https://api.spotify.com/v1"
)

// Track is the subset of Spotify's track data this app actually uses.
type Track struct {
	Artist     string
	Title      string
	Year       int
	ArtworkURL string
}

type cacheEntry struct {
	track   Track
	expires time.Time
}

// Client holds Client Credentials Flow state: the cached app access token
// (shared across all requests — it's not tied to any user) and a circuit
// breaker over the token endpoint.
type Client struct {
	clientID     string
	clientSecret string

	mu          sync.Mutex
	token       string
	tokenExpiry time.Time

	// Circuit breaker: after enough consecutive auth failures (bad/expired
	// client secret), stop calling Spotify's token endpoint on every single
	// /api/resolve request for a cooldown window and fail fast instead —
	// callers fall back to the existing multi-provider metadata fanout,
	// exactly as if this integration were never configured.
	consecutiveFailures int
	circuitOpenUntil    time.Time

	cacheMu sync.RWMutex
	cache   map[string]cacheEntry
}

const (
	maxConsecutiveFailures = 3
	circuitCooldown        = 10 * time.Minute
	trackCacheTTL          = 7 * 24 * time.Hour
)

// New returns nil if SPOTIFY_CLIENT_ID/SPOTIFY_CLIENT_SECRET aren't both
// set in the environment.
func New() *Client {
	id := os.Getenv("SPOTIFY_CLIENT_ID")
	secret := os.Getenv("SPOTIFY_CLIENT_SECRET")
	if id == "" || secret == "" {
		return nil
	}
	return &Client{clientID: id, clientSecret: secret, cache: make(map[string]cacheEntry)}
}

// GetTrack fetches Spotify's own catalog data for an exact track ID — the
// same ID already resolved from the QR code, so unlike the other metadata
// providers (which fuzzy-match on artist/title text and can land on a
// re-recording or cover) this is authoritative for that specific track.
func (c *Client) GetTrack(ctx context.Context, trackID string) (*Track, error) {
	if c == nil {
		return nil, fmt.Errorf("spotifyapi: not configured")
	}

	c.cacheMu.RLock()
	if e, ok := c.cache[trackID]; ok && time.Now().Before(e.expires) {
		c.cacheMu.RUnlock()
		t := e.track
		return &t, nil
	}
	c.cacheMu.RUnlock()

	token, err := c.getToken(ctx)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBaseURL+"/tracks/"+url.PathEscape(trackID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		// The token we thought was valid got rejected anyway (e.g. the
		// underlying app was disabled/revoked mid-lifetime) — route through
		// the same failure counter as the token endpoint so the circuit
		// breaker trips on this path too, not only on token acquisition.
		c.recordFailure()
		return nil, fmt.Errorf("spotifyapi: track request unauthorized")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spotifyapi: track request returned %d", resp.StatusCode)
	}

	var t struct {
		Name    string `json:"name"`
		Artists []struct {
			Name string `json:"name"`
		} `json:"artists"`
		Album struct {
			ReleaseDate string `json:"release_date"`
			Images      []struct {
				URL string `json:"url"`
			} `json:"images"`
		} `json:"album"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, err
	}

	track := Track{Title: t.Name}
	if len(t.Artists) > 0 {
		track.Artist = t.Artists[0].Name
	}
	// release_date is "YYYY", "YYYY-MM", or "YYYY-MM-DD" depending on
	// release_date_precision — the year is always the first 4 characters
	// regardless of which.
	if len(t.Album.ReleaseDate) >= 4 {
		_, _ = fmt.Sscanf(t.Album.ReleaseDate[:4], "%d", &track.Year)
	}
	if len(t.Album.Images) > 0 {
		track.ArtworkURL = t.Album.Images[0].URL
	}

	c.cacheMu.Lock()
	c.cache[trackID] = cacheEntry{track: track, expires: time.Now().Add(trackCacheTTL)}
	c.cacheMu.Unlock()

	return &track, nil
}

func (c *Client) getToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	if time.Now().Before(c.circuitOpenUntil) {
		until := c.circuitOpenUntil
		c.mu.Unlock()
		return "", fmt.Errorf("spotifyapi: circuit open until %s after repeated auth failures", until.Format(time.RFC3339))
	}
	if c.token != "" && time.Now().Before(c.tokenExpiry) {
		tok := c.token
		c.mu.Unlock()
		return tok, nil
	}
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL,
		strings.NewReader(url.Values{"grant_type": {"client_credentials"}}.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.clientID, c.clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		c.recordFailure()
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		c.recordFailure()
		// Almost always means SPOTIFY_CLIENT_ID/SPOTIFY_CLIENT_SECRET is
		// wrong, revoked, or stale — logged loudly since nothing else
		// notices this until someone happens to check logs, and the
		// integration otherwise fails silently into "acts as unconfigured".
		slog.Error("spotifyapi: client-credentials token request failed", "status", resp.StatusCode)
		return "", fmt.Errorf("spotifyapi: token request returned %d", resp.StatusCode)
	}

	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		c.recordFailure()
		return "", err
	}

	c.mu.Lock()
	c.token = tr.AccessToken
	// Refresh a little early so a request landing right at expiry never
	// races a 401 against a token that's technically still valid.
	c.tokenExpiry = time.Now().Add(time.Duration(tr.ExpiresIn)*time.Second - 60*time.Second)
	c.consecutiveFailures = 0
	c.circuitOpenUntil = time.Time{}
	c.mu.Unlock()

	return tr.AccessToken, nil
}

func (c *Client) recordFailure() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecutiveFailures++
	if c.consecutiveFailures >= maxConsecutiveFailures {
		c.circuitOpenUntil = time.Now().Add(circuitCooldown)
		slog.Error("spotifyapi: opening circuit breaker after repeated auth failures — check SPOTIFY_CLIENT_ID/SPOTIFY_CLIENT_SECRET",
			"consecutive_failures", c.consecutiveFailures, "cooldown_until", c.circuitOpenUntil)
	}
}
