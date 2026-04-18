package resolver

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var resolverHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        20,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     30 * time.Second,
	},
}

const maxGamesetBodySize = 32 << 20 // 32 MB

const (
	assetsBase   = "https://stgroupprdhitster.blob.core.windows.net/hitster-assets"
	gamesetDB    = assetsBase + "/gameset_database.json"
	timestampURL = assetsBase + "/gamedata_timestamp.json"
	refreshEvery = time.Hour
)

var qrPattern = regexp.MustCompile(`hitstergame\.com/[^/]+/([a-zA-Z0-9]+)/(\d+)`)

type card struct {
	CardNumber string `json:"CardNumber"`
	Spotify    string `json:"Spotify"`
}

type gamesetData struct {
	Language string `json:"gameset_language"`
	Name     string `json:"gameset_name"`
	Cards    []card `json:"cards"`
}

type gameset struct {
	SKU  string      `json:"sku"`
	Data gamesetData `json:"gameset_data"`
}

type database struct {
	UpdatedOn int64     `json:"updated_on"`
	Gamesets  []gameset `json:"gamesets"`
}

type Resolver struct {
	mu        sync.RWMutex
	lookup    map[string]string
	timestamp int64
}

func New() *Resolver {
	r := &Resolver{}
	r.lookup = make(map[string]string)
	if err := r.load(); err != nil {
		slog.Error("initial db load failed", "err", err)
	}
	go r.refreshLoop()
	return r
}

func (r *Resolver) Resolve(rawURL string) (string, error) {
	m := qrPattern.FindStringSubmatch(rawURL)
	if m == nil {
		return "", fmt.Errorf("not a valid Hitster URL")
	}
	deckID := strings.ToLower(m[1])
	cardID := m[2]
	if n, err := strconv.Atoi(m[2]); err == nil {
		cardID = fmt.Sprintf("%05d", n)
	} else {
		if len(cardID) < 5 {
			cardID = strings.Repeat("0", 5-len(cardID)) + cardID
		}
	}
	key := deckID + ":" + cardID

	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.lookup[key]
	if !ok {
		return "", fmt.Errorf("card not found: deck=%s card=%s", deckID, cardID)
	}
	return id, nil
}

func (r *Resolver) load() error {
	slog.Info("loading gameset database")
	resp, err := resolverHTTPClient.Get(gamesetDB)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gameset DB returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGamesetBodySize))
	if err != nil {
		return err
	}
	var db database
	if err := json.Unmarshal(body, &db); err != nil {
		return err
	}
	lookup := make(map[string]string, 12000)
	for _, gs := range db.Gamesets {
		sku := strings.ToLower(gs.SKU)
		for _, c := range gs.Data.Cards {
			lookup[sku+":"+c.CardNumber] = c.Spotify
		}
	}
	r.mu.Lock()
	r.lookup = lookup
	r.timestamp = db.UpdatedOn
	r.mu.Unlock()
	slog.Info("gameset database loaded", "cards", len(lookup), "gamesets", len(db.Gamesets))
	return nil
}

func (r *Resolver) refreshLoop() {
	ticker := time.NewTicker(refreshEvery)
	defer ticker.Stop()
	for range ticker.C {
		ts, err := fetchTimestamp()
		if err != nil {
			slog.Warn("timestamp check failed", "err", err)
			continue
		}
		r.mu.RLock()
		current := r.timestamp
		r.mu.RUnlock()
		if ts != current {
			slog.Info("database changed, reloading", "old", current, "new", ts)
			if err := r.load(); err != nil {
				slog.Error("reload failed", "err", err)
			}
		}
	}
}

func fetchTimestamp() (int64, error) {
	resp, err := resolverHTTPClient.Get(timestampURL)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("timestamp endpoint returned %d", resp.StatusCode)
	}
	var v struct {
		Timestamp int64 `json:"timestamp"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return 0, err
	}
	return v.Timestamp, nil
}

func SpotifyURL(id string) string {
	return "https://open.spotify.com/track/" + id
}

func StreamingLinks(artist, title string) map[string]string {
	q := url.QueryEscape(artist + " " + title)
	return map[string]string{
		"youtube_music": "https://music.youtube.com/search?q=" + q,
		"youtube":       "https://www.youtube.com/results?search_query=" + q,
		"tidal":         "https://tidal.com/search?q=" + q,
		"deezer":        "https://www.deezer.com/search/" + q,
	}
}
