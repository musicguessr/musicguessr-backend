package deck

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/musicguessr/musicguessr-backend/internal/metadata"
	"github.com/musicguessr/musicguessr-backend/internal/youtube"
)

type validateResponse struct {
	Valid   bool   `json:"valid"`
	YtID    string `json:"yt_id,omitempty"`
	Title   string `json:"title,omitempty"`
	Artist  string `json:"artist,omitempty"`
	Year    int    `json:"year,omitempty"`
	Artwork string `json:"artwork,omitempty"`
	Error   string `json:"error,omitempty"`
}

var invClient = &http.Client{
	Timeout: 6 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     30 * time.Second,
	},
}

func ValidateYtHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	raw := r.URL.Query().Get("url")
	if raw == "" {
		writeJSON(w, http.StatusBadRequest, validateResponse{Valid: false, Error: "missing url parameter"})
		return
	}
	if len(raw) > 512 {
		writeJSON(w, http.StatusBadRequest, validateResponse{Valid: false, Error: "url too long"})
		return
	}

	ytID, err := extractYtID(raw)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, validateResponse{Valid: false, Error: err.Error()})
		return
	}

	title, artist, err := fetchInvidiousVideoMeta(r.Context(), ytID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, validateResponse{Valid: false, Error: "video not found or unavailable"})
		return
	}

	normArtist := normalizeYTAuthor(artist)
	normTitle := normalizeYTTitle(title)

	resp := validateResponse{Valid: true, YtID: ytID, Title: title, Artist: normArtist}

	searchArtist := normArtist
	searchTitle := normTitle
	if searchTitle == "" {
		searchTitle = title
	}
	if searchArtist != "" || searchTitle != "" {
		if track, err := metadata.Resolve(r.Context(), searchArtist, searchTitle); err == nil {
			resp.Year = track.Year
			resp.Artwork = track.ArtworkURL
			if track.Title != "" {
				resp.Title = track.Title
			}
			if track.Artist != "" {
				resp.Artist = track.Artist
			}
		} else {
			slog.Debug("metadata enrich skipped for validate-yt", "ytID", ytID, "err", err)
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

type invidiousVideoMeta struct {
	Title  string `json:"title"`
	Author string `json:"author"`
}

func fetchInvidiousVideoMeta(ctx context.Context, ytID string) (title, artist string, err error) {
	for _, inst := range youtube.Instances() {
		reqURL := inst + "/api/v1/videos/" + ytID + "?fields=title,author"
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if e != nil {
			continue
		}
		req.Header.Set("User-Agent", "musicguessr/1.0")
		resp, e := invClient.Do(req)
		if e != nil {
			slog.Warn("invidious video meta failed", "instance", inst, "err", e)
			continue
		}
		if resp.StatusCode == http.StatusNotFound {
			_ = resp.Body.Close()
			return "", "", fmt.Errorf("video not found")
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			continue
		}
		var meta invidiousVideoMeta
		e = json.NewDecoder(resp.Body).Decode(&meta)
		_ = resp.Body.Close()
		if e != nil {
			continue
		}
		return meta.Title, meta.Author, nil
	}
	return "", "", fmt.Errorf("all invidious instances failed for %s", ytID)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("writeJSON encode failed", "err", err)
	}
}
