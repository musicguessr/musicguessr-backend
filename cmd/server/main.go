package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/musicguessr/musicguessr-backend/internal/metadata"
	"github.com/musicguessr/musicguessr-backend/internal/resolver"
	"github.com/musicguessr/musicguessr-backend/internal/youtube"
)

type resolveResponse struct {
	SpotifyID      string            `json:"spotify_id"`
	SpotifyURL     string            `json:"spotify_url"`
	Artist         string            `json:"artist,omitempty"`
	Title          string            `json:"title,omitempty"`
	Year           int               `json:"year,omitempty"`
	ArtworkURL     string            `json:"artwork_url,omitempty"`
	YouTubeVideoID string            `json:"youtube_video_id,omitempty"`
	Links          map[string]string `json:"links"`
}

type errResponse struct {
	Error string `json:"error"`
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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
	json.NewEncoder(w).Encode(v)
}

func main() {
	if os.Getenv("LOG_LEVEL") == "debug" {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	res := resolver.New()
	var httpClient = &http.Client{Timeout: 8 * time.Second}
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/api/resolve", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		qrURL := r.URL.Query().Get("url")
		if qrURL == "" {
			writeJSON(w, http.StatusBadRequest, errResponse{"missing url parameter"})
			return
		}

		spotifyID, err := res.Resolve(qrURL)
		if err != nil {
			writeJSON(w, http.StatusNotFound, errResponse{err.Error()})
			return
		}

		resp := resolveResponse{
			SpotifyID:  spotifyID,
			SpotifyURL: resolver.SpotifyURL(spotifyID),
			Links:      make(map[string]string),
		}

		// Enrich: Spotify oEmbed → artist/title
		artist, title := fetchSpotifyMeta(r.Context(), httpClient, spotifyID)
		if artist != "" {
			// ytArtist/ytTitle start as Spotify values; iTunes may overwrite with cleaner metadata.
			ytArtist, ytTitle := artist, title

			// Try metadata provider chain (parallel providers)
			if track, err := metadata.Resolve(r.Context(), artist, title); err == nil {
				resp.Artist = track.Artist
				resp.Title = track.Title
				resp.Year = track.Year
				resp.ArtworkURL = track.ArtworkURL
				if track.AppleMusicURL != "" {
					resp.Links["apple_music"] = track.AppleMusicURL
				}
				resp.Links = resolver.StreamingLinks(track.Artist, track.Title)
				ytArtist, ytTitle = track.Artist, track.Title
			} else {
				slog.Warn("metadata resolve failed", "artist", artist, "title", title, "err", err)
				resp.Artist = artist
				resp.Title = title
				resp.Links = resolver.StreamingLinks(artist, title)
			}

			// YouTube video ID via Invidious — always attempt, even when iTunes fails.
			allowVariants := r.URL.Query().Get("yt_variants") == "1"
			if videoID, err := youtube.SearchVideoID(r.Context(), ytArtist, ytTitle, allowVariants); err == nil {
				resp.YouTubeVideoID = videoID
			} else {
				slog.Warn("youtube search failed", "artist", ytArtist, "title", ytTitle, "err", err)
			}
		}
		resp.Links["spotify"] = resp.SpotifyURL

		writeJSON(w, http.StatusOK, resp)
	})

	handler := cors(mux)
	srv := &http.Server{Addr: ":" + port, Handler: handler}

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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
}

func fetchSpotifyMeta(ctx context.Context, client *http.Client, trackID string) (artist, title string) {
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
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Error("spotify page non-200", "trackID", trackID, "status", resp.StatusCode)
		return "", ""
	}

	// read up to first 32KB of the response
	limited := io.LimitReader(resp.Body, 32*1024)
	body, _ := io.ReadAll(limited)
	html := string(body)

	// og:title → track title. Spotify uses several formats:
	//   "Track Name - song by Artist | Spotify"          (most common)
	//   "Track Name - song and lyrics by Artist | Spotify"
	//   "Track Name | Spotify"
	//   "Track Name - Radio edit"                        (no Spotify suffix — use raw)
	const ogTitleNeedle = `og:title" content="`
	if idx := strings.Index(html, ogTitleNeedle); idx != -1 {
		start := idx + len(ogTitleNeedle)
		if end := strings.Index(html[start:], `"`); end != -1 {
			raw := decodeHTMLEntities(html[start : start+end])
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
	if idx := strings.Index(html, ogDescNeedle); idx != -1 {
		start := idx + len(ogDescNeedle)
		if end := strings.Index(html[start:], `"`); end != -1 {
			desc := decodeHTMLEntities(html[start : start+end])
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
	return
}

// decodeHTMLEntities replaces common HTML entities with their UTF-8 equivalents.
func decodeHTMLEntities(s string) string {
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&apos;", "'")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	return s
}
