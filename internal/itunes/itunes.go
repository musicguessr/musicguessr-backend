package itunes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const searchURL = "https://itunes.apple.com/search"

type Track struct {
	Artist        string `json:"artist"`
	Title         string `json:"title"`
	Year          int    `json:"year"`
	AppleMusicURL string `json:"apple_music_url"`
	ArtworkURL    string `json:"artwork_url"`
}

type result struct {
	ResultCount int `json:"resultCount"`
	Results     []struct {
		ArtistName    string `json:"artistName"`
		TrackName     string `json:"trackName"`
		TrackViewURL  string `json:"trackViewUrl"`
		ArtworkURL100 string `json:"artworkUrl100"`
		ReleaseDate   string `json:"releaseDate"`
	} `json:"results"`
}

var client = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     30 * time.Second,
	},
}

func Search(ctx context.Context, artist, title string) (*Track, error) {
	vals := url.Values{}
	vals.Set("term", artist+" "+title)
	vals.Set("media", "music")
	vals.Set("limit", "3")
	reqURL := searchURL + "?" + vals.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("itunes search returned status %d", resp.StatusCode)
	}
	defer func() { _ = resp.Body.Close() }()

	var r result
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	if r.ResultCount == 0 {
		return nil, fmt.Errorf("no results")
	}

	item := r.Results[0]
	year := 0
	if item.ReleaseDate != "" {
		if t, err := time.Parse(time.RFC3339, item.ReleaseDate); err == nil {
			year = t.Year()
		} else if len(item.ReleaseDate) >= 4 {
			_, _ = fmt.Sscanf(item.ReleaseDate[:4], "%d", &year)
		}
	}

	artwork := strings.Replace(item.ArtworkURL100, "100x100", "300x300", 1)

	return &Track{
		Artist:        item.ArtistName,
		Title:         item.TrackName,
		Year:          year,
		AppleMusicURL: item.TrackViewURL,
		ArtworkURL:    artwork,
	}, nil
}
