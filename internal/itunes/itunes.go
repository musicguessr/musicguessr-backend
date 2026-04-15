package itunes

import (
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

var client = &http.Client{Timeout: 5 * time.Second}

func Search(artist, title string) (*Track, error) {
	q := url.QueryEscape(artist + " " + title)
	resp, err := client.Get(fmt.Sprintf("%s?term=%s&media=music&limit=3", searchURL, q))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var r result
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	if r.ResultCount == 0 {
		return nil, fmt.Errorf("no results")
	}

	item := r.Results[0]
	year := 0
	if len(item.ReleaseDate) >= 4 {
		fmt.Sscanf(item.ReleaseDate[:4], "%d", &year)
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
