package spotifyapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, tokenHandler, apiHandler http.HandlerFunc) *Client {
	t.Helper()
	tokenSrv := httptest.NewServer(tokenHandler)
	apiSrv := httptest.NewServer(apiHandler)
	t.Cleanup(func() {
		tokenSrv.Close()
		apiSrv.Close()
	})

	origToken, origAPI := tokenURL, apiBaseURL
	tokenURL, apiBaseURL = tokenSrv.URL, apiSrv.URL
	t.Cleanup(func() { tokenURL, apiBaseURL = origToken, origAPI })

	return &Client{clientID: "id", clientSecret: "secret", cache: make(map[string]cacheEntry)}
}

func jsonHandler(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

func TestNew_NotConfiguredReturnsNil(t *testing.T) {
	t.Setenv("SPOTIFY_CLIENT_ID", "")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "")
	if c := New(); c != nil {
		t.Fatalf("got %+v, want nil when env vars are unset", c)
	}
}

func TestGetTrack_NilClientReturnsError(t *testing.T) {
	var c *Client
	if _, err := c.GetTrack(context.Background(), "abc"); err == nil {
		t.Fatal("expected an error from a nil client, got none")
	}
}

func TestGetTrack_Success(t *testing.T) {
	c := newTestClient(t,
		jsonHandler(map[string]any{"access_token": "tok123", "expires_in": 3600}),
		jsonHandler(map[string]any{
			"name":    "Charlie's Angels Theme",
			"artists": []map[string]string{{"name": "Jack Elliot"}},
			"album": map[string]any{
				"release_date": "1976-09-22",
				"images":       []map[string]string{{"url": "https://example.com/art.jpg"}},
			},
		}),
	)

	track, err := c.GetTrack(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}
	if track.Artist != "Jack Elliot" || track.Title != "Charlie's Angels Theme" || track.Year != 1976 {
		t.Fatalf("got %+v, want Jack Elliot / Charlie's Angels Theme / 1976", track)
	}
	if track.ArtworkURL != "https://example.com/art.jpg" {
		t.Fatalf("got artwork %q", track.ArtworkURL)
	}
}

func TestGetTrack_CachesResult(t *testing.T) {
	var apiCalls int32
	c := newTestClient(t,
		jsonHandler(map[string]any{"access_token": "tok123", "expires_in": 3600}),
		func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&apiCalls, 1)
			jsonHandler(map[string]any{"name": "T", "artists": []map[string]string{{"name": "A"}}})(w, r)
		},
	)

	for i := 0; i < 3; i++ {
		if _, err := c.GetTrack(context.Background(), "same-id"); err != nil {
			t.Fatalf("GetTrack call %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&apiCalls); got != 1 {
		t.Fatalf("got %d api calls, want 1 (subsequent calls should hit the cache)", got)
	}
}

func TestGetTrack_TokenFailureDoesNotPanicAndReturnsError(t *testing.T) {
	c := newTestClient(t,
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
		jsonHandler(map[string]any{}),
	)

	if _, err := c.GetTrack(context.Background(), "abc"); err == nil {
		t.Fatal("expected an error when the token endpoint rejects the credentials")
	}
}

func TestCircuitBreaker_OpensAfterRepeatedAuthFailures(t *testing.T) {
	var tokenCalls int32
	c := newTestClient(t,
		func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&tokenCalls, 1)
			w.WriteHeader(http.StatusUnauthorized)
		},
		jsonHandler(map[string]any{}),
	)

	for i := 0; i < maxConsecutiveFailures; i++ {
		if _, err := c.GetTrack(context.Background(), "id"); err == nil {
			t.Fatalf("call %d: expected error", i)
		}
	}
	if got := atomic.LoadInt32(&tokenCalls); got != maxConsecutiveFailures {
		t.Fatalf("got %d token calls before circuit should open, want %d", got, maxConsecutiveFailures)
	}

	// One more call: the circuit should now be open, so it must fail fast
	// without hitting the token endpoint again.
	if _, err := c.GetTrack(context.Background(), "id2"); err == nil {
		t.Fatal("expected error while circuit is open")
	}
	if got := atomic.LoadInt32(&tokenCalls); got != maxConsecutiveFailures {
		t.Fatalf("got %d token calls, want still %d — circuit breaker should have short-circuited", got, maxConsecutiveFailures)
	}
}

func TestCircuitBreaker_ClosesAfterCooldown(t *testing.T) {
	c := newTestClient(t,
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
		jsonHandler(map[string]any{}),
	)

	for i := 0; i < maxConsecutiveFailures; i++ {
		_, _ = c.GetTrack(context.Background(), "id")
	}
	c.mu.Lock()
	if c.circuitOpenUntil.IsZero() {
		c.mu.Unlock()
		t.Fatal("expected circuit to be open after repeated failures")
	}
	// Simulate the cooldown having already elapsed instead of sleeping in
	// the test.
	c.circuitOpenUntil = time.Now().Add(-time.Second)
	c.mu.Unlock()

	tokenSrv := httptest.NewServer(jsonHandler(map[string]any{"access_token": "tok", "expires_in": 3600}))
	t.Cleanup(tokenSrv.Close)
	tokenURL = tokenSrv.URL

	if _, err := c.GetTrack(context.Background(), "id3"); err != nil {
		t.Fatalf("expected the circuit to allow a retry after cooldown, got: %v", err)
	}
}
