package deck

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

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
		if errors.Is(err, youtube.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, validateResponse{Valid: false, Error: "video not found or unavailable"})
			return
		}
		// Every instance failed for infrastructure reasons (network/5xx/bad
		// JSON) — this is not evidence the video/URL itself is invalid, so it
		// must not be reported as 404 "not found" (misleads the user into
		// thinking their valid link is bad).
		slog.Warn("validate-yt: all invidious instances failed", "ytID", ytID, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, validateResponse{Valid: false, Error: "could not verify video right now, try again shortly"})
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
	var meta invidiousVideoMeta
	err = youtube.FetchJSON(ctx, func(inst string) string {
		return inst + "/api/v1/videos/" + ytID + "?fields=title,author"
	}, &meta)
	if err != nil {
		return "", "", err
	}
	return meta.Title, meta.Author, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("writeJSON encode failed", "err", err)
	}
}
