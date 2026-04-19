package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/musicguessr/musicguessr-backend/internal/itunes"
)

var httpClient = &http.Client{
	Timeout: 6 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     30 * time.Second,
	},
}
var musicbrainzUserAgent = "musicguessr/0.1 (https://github.com/musicguessr)"
var cacheTTL = 24 * time.Hour

func init() {
	if s := os.Getenv("METADATA_CACHE_TTL_SECONDS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			cacheTTL = time.Duration(n) * time.Second
		}
	}
	// default in-memory cache; can be replaced via SetCache
	defaultCache = NewMemoryCache(cacheTTL)
}

// Cache interface allows swapping to Redis/Valkey later.
type Cache interface {
	Get(key string) (*itunes.Track, bool)
	Set(key string, t *itunes.Track, ttl time.Duration)
}

var defaultCache Cache

// SetCache replaces the package cache (useful to inject Redis-backed cache).
func SetCache(c Cache) {
	if c != nil {
		defaultCache = c
	}
}

// Resolve tries providers in parallel and returns the first complete result.
// Caller should provide a context (e.g. request context).
func Resolve(ctx context.Context, artist, title string) (*itunes.Track, error) {
	// overall timeout
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	key := normalizeKey(artist) + "|" + normalizeKey(title)
	if defaultCache != nil {
		if v, ok := defaultCache.Get(key); ok {
			return v, nil
		}
	}

	type pres struct {
		t   *itunes.Track
		src string
	}

	providers := []struct {
		name string
		fn   func(context.Context, string, string) (*itunes.Track, error)
	}{
		{"itunes", func(c context.Context, a, t string) (*itunes.Track, error) { return itunes.Search(c, a, t) }},
		{"musicbrainz", musicBrainzSearch},
		{"deezer", deezerSearch},
		{"discogs", discogsSearch},
		{"theaudiodb", theAudioDBSearch},
	}

	var wg sync.WaitGroup
	ch := make(chan pres, len(providers))
	for _, p := range providers {
		wg.Add(1)
		go func(name string, fn func(context.Context, string, string) (*itunes.Track, error)) {
			defer wg.Done()
			t, err := fn(ctx, artist, title)
			if err != nil || t == nil {
				return
			}
			// basic requirement: at least artist or title
			if t.Artist == "" && t.Title == "" {
				return
			}
			// verify artwork availability
			if t.ArtworkURL != "" {
				if ok := checkArtwork(ctx, t.ArtworkURL); !ok {
					t.ArtworkURL = ""
				}
			}
			select {
			case ch <- pres{t: t, src: name}:
			case <-ctx.Done():
			}
		}(p.name, p.fn)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	results := make([]pres, 0, len(providers))
	for r := range ch {
		results = append(results, r)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("no metadata found for %q - %q", artist, title)
	}

	// Aggregate fields
	artists := make([]string, 0, len(results))
	titles := make([]string, 0, len(results))
	years := make([]int, 0, len(results))
	apples := make([]string, 0, len(results))
	artworks := make([]string, 0, len(results))
	for _, r := range results {
		if r.t.Artist != "" {
			artists = append(artists, normalizeKey(r.t.Artist))
		}
		if r.t.Title != "" {
			titles = append(titles, normalizeKey(r.t.Title))
		}
		if r.t.Year != 0 {
			years = append(years, r.t.Year)
		}
		if r.t.AppleMusicURL != "" {
			apples = append(apples, r.t.AppleMusicURL)
		}
		if r.t.ArtworkURL != "" {
			artworks = append(artworks, r.t.ArtworkURL)
		}
	}

	artistFinal := chooseMostCommon(artists)
	titleFinal := chooseMostCommon(titles)
	yearFinal := chooseMostCommonInt(years)
	artworkFinal := chooseArtworkPreferred(artworks)
	appleFinal := firstNonEmpty(apples...)

	// Return normalized Track but preserve original casing for title/artist where possible
	// Attempt to pick original casing from any result that matches normalized string
	pickOriginal := func(origs []pres, key string, field string) string {
		for _, p := range origs {
			var val string
			if field == "artist" {
				val = p.t.Artist
			} else {
				val = p.t.Title
			}
			if normalizeKey(val) == key {
				return val
			}
		}
		return ""
	}

	artistOut := pickOriginal(results, artistFinal, "artist")
	if artistOut == "" {
		artistOut = artist
	}
	titleOut := pickOriginal(results, titleFinal, "title")
	if titleOut == "" {
		titleOut = title
	}

	out := &itunes.Track{
		Artist:        artistOut,
		Title:         titleOut,
		Year:          yearFinal,
		AppleMusicURL: appleFinal,
		ArtworkURL:    artworkFinal,
	}

	if defaultCache != nil {
		defaultCache.Set(key, out, cacheTTL)
	}

	return out, nil
}

func normalizeKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// collapse whitespace
	s = strings.Join(strings.Fields(s), " ")
	return s
}

func chooseMostCommon(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, s := range ss {
		counts[s]++
	}
	type kv struct {
		k string
		v int
	}
	var arr []kv
	for k, v := range counts {
		arr = append(arr, kv{k, v})
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].v > arr[j].v })
	return arr[0].k
}

func chooseMostCommonInt(nums []int) int {
	if len(nums) == 0 {
		return 0
	}
	counts := map[int]int{}
	for _, n := range nums {
		counts[n]++
	}
	best, bestc := 0, 0
	for k, v := range counts {
		if v > bestc || (v == bestc && k > best) {
			best = k
			bestc = v
		}
	}
	return best
}

func chooseArtworkPreferred(urls []string) string {
	if len(urls) == 0 {
		return ""
	}
	// priority: coverartarchive, discogs, deezer, theaudiodb, itunes
	for _, u := range urls {
		if strings.Contains(u, "coverartarchive.org") {
			return u
		}
	}
	for _, u := range urls {
		if strings.Contains(u, "discogs") {
			return u
		}
	}
	for _, u := range urls {
		if strings.Contains(u, "deezer") || strings.Contains(u, "cover_big") {
			return u
		}
	}
	return urls[0]
}

// musicBrainzSearch queries the MusicBrainz recordings endpoint for a match.
func musicBrainzSearch(ctx context.Context, artist, title string) (*itunes.Track, error) {
	q := fmt.Sprintf(`recording:"%s" AND artist:"%s"`, title, artist)
	reqURL := "https://musicbrainz.org/ws/2/recording?fmt=json&limit=3&query=" + url.QueryEscape(q)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", musicbrainzUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("musicbrainz status %d", resp.StatusCode)
	}

	var mb struct {
		Recordings []struct {
			ID           string `json:"id"`
			Title        string `json:"title"`
			ArtistCredit []struct {
				Name string `json:"name"`
			} `json:"artist-credit"`
			Releases []struct {
				ID   string `json:"id"`
				Date string `json:"date"`
			} `json:"releases"`
		} `json:"recordings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&mb); err != nil {
		return nil, err
	}
	if len(mb.Recordings) == 0 {
		return nil, fmt.Errorf("musicbrainz: no recordings")
	}

	rec := mb.Recordings[0]
	art := ""
	if len(rec.ArtistCredit) > 0 {
		art = rec.ArtistCredit[0].Name
	}
	titleRes := rec.Title
	year := 0
	artwork := ""
	if len(rec.Releases) > 0 {
		rel := rec.Releases[0]
		if rel.Date != "" && len(rel.Date) >= 4 {
			var y int
			_, _ = fmt.Sscanf(rel.Date[:4], "%d", &y)
			year = y
		}
		// Construct Cover Art Archive URL (may 404 when not available)
		artwork = "https://coverartarchive.org/release/" + url.PathEscape(rec.Releases[0].ID) + "/front"
	}

	return &itunes.Track{
		Artist:        art,
		Title:         titleRes,
		Year:          year,
		AppleMusicURL: "",
		ArtworkURL:    artwork,
	}, nil
}

// deezerSearch queries the Deezer public search API.
func deezerSearch(ctx context.Context, artist, title string) (*itunes.Track, error) {
	q := fmt.Sprintf(`artist:"%s" track:"%s"`, artist, title)
	reqURL := "https://api.deezer.com/search?q=" + url.QueryEscape(q) + "&limit=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("deezer status %d", resp.StatusCode)
	}

	var d struct {
		Data []struct {
			Title  string `json:"title"`
			Link   string `json:"link"`
			Artist struct {
				Name string `json:"name"`
			} `json:"artist"`
			Album struct {
				ID       int    `json:"id"`
				CoverBig string `json:"cover_big"`
			} `json:"album"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}
	if len(d.Data) == 0 {
		return nil, fmt.Errorf("deezer: no results")
	}
	item := d.Data[0]

	year := 0
	if item.Album.ID != 0 {
		if y := deezerAlbumYear(ctx, item.Album.ID); y != 0 {
			year = y
		}
	}

	return &itunes.Track{
		Artist:        item.Artist.Name,
		Title:         item.Title,
		Year:          year,
		AppleMusicURL: "",
		ArtworkURL:    item.Album.CoverBig,
	}, nil
}

func deezerAlbumYear(ctx context.Context, albumID int) int {
	reqURL := fmt.Sprintf("https://api.deezer.com/album/%d", albumID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var a struct {
		ReleaseDate string `json:"release_date"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		return 0
	}
	year := 0
	if len(a.ReleaseDate) >= 4 {
		_, _ = fmt.Sscanf(a.ReleaseDate[:4], "%d", &year)
	}
	return year
}

// discogsSearch queries Discogs DB when DISCOGS_TOKEN is present in env.
func discogsSearch(ctx context.Context, artist, title string) (*itunes.Track, error) {
	token := os.Getenv("DISCOGS_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("discogs: no token configured")
	}
	q := fmt.Sprintf(`artist:"%s" track:"%s"`, artist, title)
	reqURL := "https://api.discogs.com/database/search?q=" + url.QueryEscape(q) + "&type=release&per_page=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	// Discogs supports token via ?token= or Authorization header; use header
	req.Header.Set("Authorization", "Discogs token="+token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discogs status %d", resp.StatusCode)
	}

	var d struct {
		Results []struct {
			Title string `json:"title"`
			Year  int    `json:"year"`
			Thumb string `json:"thumb"`
			Cover string `json:"cover_image"`
			URI   string `json:"resource_url"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}
	if len(d.Results) == 0 {
		return nil, fmt.Errorf("discogs: no results")
	}
	it := d.Results[0]
	// Discogs Title often includes artist - title; best-effort split skipped here
	return &itunes.Track{
		Artist:        artist,
		Title:         title,
		Year:          it.Year,
		AppleMusicURL: "",
		ArtworkURL:    firstNonEmpty(it.Cover, it.Thumb),
	}, nil
}

// theAudioDBSearch queries TheAudioDB (requires THEAUDIODB_KEY env or '1')
func theAudioDBSearch(ctx context.Context, artist, title string) (*itunes.Track, error) {
	key := os.Getenv("THEAUDIODB_KEY")
	if key == "" {
		key = "1"
	}
	reqURL := "https://theaudiodb.com/api/v1/json/" + url.PathEscape(key) + "/searchtrack.php?s=" + url.QueryEscape(artist) + "&t=" + url.QueryEscape(title)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("theaudiodb status %d", resp.StatusCode)
	}

	var t struct {
		Track []struct {
			StrTrack      string `json:"strTrack"`
			StrArtist     string `json:"strArtist"`
			IntYear       string `json:"intYearReleased"`
			StrTrackThumb string `json:"strTrackThumb"`
		} `json:"track"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, err
	}
	if len(t.Track) == 0 {
		return nil, fmt.Errorf("theaudiodb: no results")
	}
	item := t.Track[0]
	year := 0
	if item.IntYear != "" {
		_, _ = fmt.Sscanf(item.IntYear, "%d", &year)
	}
	return &itunes.Track{
		Artist:        item.StrArtist,
		Title:         item.StrTrack,
		Year:          year,
		AppleMusicURL: "",
		ArtworkURL:    item.StrTrackThumb,
	}, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// checkArtwork validates artwork availability. Only Cover Art Archive URLs are checked
// via HEAD request since they frequently 404. Other CDN providers (iTunes, Deezer,
// TheAudioDB, Discogs) are assumed to be stable and skipped to save latency.
func checkArtwork(ctx context.Context, u string) bool {
	if !strings.Contains(u, "coverartarchive.org") {
		return true
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return false
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// --- simple in-memory cache implementation ---
type memEntry struct {
	t       *itunes.Track
	expires time.Time
}

type MemoryCache struct {
	mu  sync.RWMutex
	m   map[string]memEntry
	ttl time.Duration
}

func NewMemoryCache(ttl time.Duration) *MemoryCache {
	return &MemoryCache{m: make(map[string]memEntry), ttl: ttl}
}

func (c *MemoryCache) Get(key string) (*itunes.Track, bool) {
	c.mu.RLock()
	e, ok := c.m[key]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expires) {
		c.mu.Lock()
		delete(c.m, key)
		c.mu.Unlock()
		return nil, false
	}
	return e.t, true
}

func (c *MemoryCache) Set(key string, t *itunes.Track, ttl time.Duration) {
	if ttl <= 0 {
		ttl = c.ttl
	}
	c.mu.Lock()
	c.m[key] = memEntry{t: t, expires: time.Now().Add(ttl)}
	c.mu.Unlock()
}
