package deck

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/musicguessr/musicguessr-backend/internal/metadata"
	"github.com/musicguessr/musicguessr-backend/internal/youtube"
)

type importPlaylistResponse struct {
	PlaylistID string             `json:"playlist_id"`
	Videos     []validateResponse `json:"videos"`
	Total      int                `json:"total"`
}

// ImportPlaylistHandler handles GET /api/deck/import-playlist?url=<youtube_playlist_url>
// Returns up to maxCards video entries from the playlist, ready to use as deck cards.
func ImportPlaylistHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	raw := r.URL.Query().Get("url")
	if raw == "" {
		writeJSON(w, http.StatusBadRequest, errResp("missing url parameter"))
		return
	}
	if len(raw) > 512 {
		writeJSON(w, http.StatusBadRequest, errResp("url too long"))
		return
	}

	playlistID, err := extractPlaylistID(raw)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errResp(err.Error()))
		return
	}

	videos, err := youtube.FetchPlaylist(r.Context(), playlistID, maxCards)
	if err != nil {
		if errors.Is(err, youtube.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errResp("playlist not found or is private"))
			return
		}
		slog.Warn("import-playlist: yt-dlp fetch failed", "playlistID", playlistID, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, errResp("could not fetch playlist right now, try again shortly"))
		return
	}
	if len(videos) == 0 {
		writeJSON(w, http.StatusNotFound, errResp("playlist is empty or all videos are unavailable"))
		return
	}

	cards := make([]validateResponse, 0, len(videos))
	for _, v := range videos {
		if v.VideoID == "" {
			continue
		}
		cards = append(cards, validateResponse{
			Valid:  true,
			YtID:   v.VideoID,
			Title:  v.Title,
			Artist: normalizeYTAuthor(v.Author),
		})
	}

	// Enrich cards concurrently: year, artwork, cleaned title/artist via metadata providers.
	// The semaphore is acquired before the goroutine is spawned (not inside it)
	// so at most enrichConcurrency goroutines exist at a time instead of up to
	// maxCards (300) all parked on the channel simultaneously.
	const enrichConcurrency = 15
	sem := make(chan struct{}, enrichConcurrency)
	var wg sync.WaitGroup
	for i := range cards {
		sem <- struct{}{}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if rec := recover(); rec != nil {
					slog.Error("playlist enrich panicked", "card", i, "panic", rec)
				}
			}()

			c := &cards[i]
			searchTitle := normalizeYTTitle(c.Title)
			if searchTitle == "" {
				searchTitle = c.Title
			}
			if c.Artist == "" && searchTitle == "" {
				return
			}

			ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
			defer cancel()

			track, err := metadata.Resolve(ctx, c.Artist, searchTitle)
			if err != nil {
				slog.Debug("playlist enrich failed", "yt_id", c.YtID, "err", err)
				return
			}
			if track.Year != 0 {
				c.Year = track.Year
			}
			if track.ArtworkURL != "" {
				c.Artwork = track.ArtworkURL
			}
			if track.Title != "" {
				c.Title = track.Title
			}
			if track.Artist != "" {
				c.Artist = track.Artist
			}
		}(i)
	}
	wg.Wait()

	writeJSON(w, http.StatusOK, importPlaylistResponse{
		PlaylistID: playlistID,
		Videos:     cards,
		Total:      len(cards),
	})
}

// extractPlaylistID parses a YouTube playlist ID from a URL or bare ID.
// Supports:
//
//	https://www.youtube.com/playlist?list=PLxxxx
//	https://youtube.com/watch?v=xxx&list=PLxxxx
//	PLxxxx (bare ID)
func extractPlaylistID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)

	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		if id := u.Query().Get("list"); id != "" && looksLikePlaylistID(id) {
			return id, nil
		}
	}

	if looksLikePlaylistID(raw) {
		return raw, nil
	}

	return "", fmt.Errorf("could not extract playlist ID — paste a YouTube playlist URL or a bare playlist ID (starts with PL, UU, etc.)")
}

func looksLikePlaylistID(s string) bool {
	// YouTube playlist ID prefixes: PL (standard), UU (uploads), FL (favorites),
	// OL (watch later), RD (mix/radio), LL (liked videos), WL (watch later)
	for _, pfx := range []string{"PL", "UU", "FL", "OL", "RD", "LL", "WL"} {
		if strings.HasPrefix(s, pfx) && len(s) > 5 {
			return true
		}
	}
	return false
}
