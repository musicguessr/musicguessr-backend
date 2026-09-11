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

type resultItem struct {
	ArtistName    string `json:"artistName"`
	TrackName     string `json:"trackName"`
	TrackViewURL  string `json:"trackViewUrl"`
	ArtworkURL100 string `json:"artworkUrl100"`
	ReleaseDate   string `json:"releaseDate"`
}

type result struct {
	ResultCount int          `json:"resultCount"`
	Results     []resultItem `json:"results"`
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
	vals.Set("limit", "5")
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
	// resultCount is the API's self-reported count, not necessarily len(Results) —
	// check the actual slice length too, or a divergence (throttling/API quirk)
	// panics on the index below instead of returning "no results".
	if r.ResultCount == 0 || len(r.Results) == 0 {
		return nil, fmt.Errorf("no results")
	}

	item := pickEarliestMatch(r.Results, artist)
	year := parseReleaseYear(item.ReleaseDate)
	artwork := strings.Replace(item.ArtworkURL100, "100x100", "300x300", 1)

	return &Track{
		Artist:        item.ArtistName,
		Title:         item.TrackName,
		Year:          year,
		AppleMusicURL: item.TrackViewURL,
		ArtworkURL:    artwork,
	}, nil
}

// pickEarliestMatch prefers the earliest-dated result whose artist plausibly
// matches the query, falling back to iTunes's own top relevance match
// (results[0]) when no other result's artist matches at all. iTunes's
// relevance ranking is not the same as "this is the original recording" —
// for older TV/film themes in particular, a newer re-recording or
// compilation cover (cheaper to license than the original master) very
// commonly outranks the original, which skews the year a Hitster-style game
// is actually asking about.
func pickEarliestMatch(results []resultItem, queryArtist string) resultItem {
	best := results[0]
	bestYear := parseReleaseYear(best.ReleaseDate)
	bestMatches := artistMatches(best.ArtistName, queryArtist)

	for _, r := range results[1:] {
		if !artistMatches(r.ArtistName, queryArtist) {
			continue
		}
		y := parseReleaseYear(r.ReleaseDate)
		if y == 0 {
			continue
		}
		if !bestMatches || bestYear == 0 || y < bestYear {
			best, bestYear, bestMatches = r, y, true
		}
	}
	return best
}

// artistMatches is a deliberately loose, case-insensitive substring check —
// iTunes formats a single artist two different ways depending on catalog
// entry ("Jack Elliot" vs "Jack Elliot & Allyn Ferguson"), and this only
// needs to rule out a result being about a clearly different artist, not
// perform exact identity matching.
func artistMatches(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	if a == "" || b == "" {
		return false
	}
	return strings.Contains(a, b) || strings.Contains(b, a)
}

func parseReleaseYear(releaseDate string) int {
	if releaseDate == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339, releaseDate); err == nil {
		return t.Year()
	}
	if len(releaseDate) >= 4 {
		var year int
		if _, err := fmt.Sscanf(releaseDate[:4], "%d", &year); err == nil {
			return year
		}
	}
	return 0
}
