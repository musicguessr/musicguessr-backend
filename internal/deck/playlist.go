package deck

import (
	"context"
	"encoding/json"
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

	videos, err := fetchPlaylistVideos(r.Context(), playlistID, maxCards)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errResp(err.Error()))
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
			Valid:   true,
			YtID:    v.VideoID,
			Title:   v.Title,
			Artist:  normalizeYTAuthor(v.Author),
		})
	}

	// Enrich cards concurrently: year, artwork, cleaned title/artist via metadata providers.
	const enrichConcurrency = 15
	sem := make(chan struct{}, enrichConcurrency)
	var wg sync.WaitGroup
	for i := range cards {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

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

type invidiousPlaylistVideo struct {
	VideoID string `json:"videoId"`
	Title   string `json:"title"`
	Author  string `json:"author"`
}

type invidiousPlaylist struct {
	Videos []invidiousPlaylistVideo `json:"videos"`
}

const maxPlaylistPages = 10

func fetchPlaylistVideos(ctx context.Context, playlistID string, limit int) ([]invidiousPlaylistVideo, error) {
	var all []invidiousPlaylistVideo
	for page := 1; page <= maxPlaylistPages && len(all) < limit; page++ {
		batch, err := fetchPlaylistPage(ctx, playlistID, page)
		if err != nil {
			if len(all) > 0 {
				// Partial results from previous pages are usable.
				break
			}
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		remaining := limit - len(all)
		if len(batch) > remaining {
			batch = batch[:remaining]
		}
		all = append(all, batch...)
		// Invidious returns ≤100 videos per page; fewer means we're on the last page.
		if len(batch) < 100 {
			break
		}
	}
	return all, nil
}

func fetchPlaylistPage(ctx context.Context, playlistID string, page int) ([]invidiousPlaylistVideo, error) {
	for _, inst := range youtube.Instances() {
		reqURL := fmt.Sprintf("%s/api/v1/playlists/%s?page=%d&fields=videos", inst, url.PathEscape(playlistID), page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "musicguessr/1.0")
		resp, err := invClient.Do(req)
		if err != nil {
			slog.Warn("invidious playlist page failed", "instance", inst, "page", page, "err", err)
			continue
		}
		if resp.StatusCode == http.StatusNotFound {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("playlist not found or is private")
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			slog.Warn("invidious playlist bad status", "instance", inst, "status", resp.StatusCode)
			continue
		}
		var pl invidiousPlaylist
		err = json.NewDecoder(resp.Body).Decode(&pl)
		_ = resp.Body.Close()
		if err != nil {
			slog.Warn("invidious playlist decode failed", "instance", inst, "err", err)
			continue
		}
		return pl.Videos, nil
	}
	return nil, fmt.Errorf("all invidious instances failed for playlist %s", playlistID)
}
