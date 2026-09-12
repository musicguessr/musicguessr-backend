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
	// hitster.jumboplay.com is Hitster/Jumbo's actual production asset host
	// (backed by Azure Blob storage, same as the alternative below) and is
	// kept current — as of writing, 52 gamesets and ~14,000 cards ahead of
	// stgroupprdhitster.blob.core.windows.net, which turned out to have
	// gone stale (10 months without an update) despite looking like the
	// "official" host. Confirmed via a manual diff against a copy from
	// github.com/joschkarick/hitster-deezer while investigating that repo.
	assetsBase   = "https://hitster.jumboplay.com/hitster-assets"
	gamesetDB    = assetsBase + "/gameset_database.json"
	refreshEvery = time.Hour
)

var (
	deckIDSegmentRe = regexp.MustCompile(`^[a-zA-Z0-9]+$`)
	cardIDSegmentRe = regexp.MustCompile(`^\d+$`)
)

// parseHitsterURL extracts the {deckId}/{cardId} segments from a
// hitstergame.com/{lang}/{deckId}/{cardId} URL. It parses the URL properly
// and checks the host exactly, rather than matching "hitstergame.com/..." as
// a bare substring anywhere in the input — the previous unanchored regex
// accepted spoofed hosts like "evilhitstergame.com/en/AB1/42" or
// "hitstergame.com.evil.tld/...".
func parseHitsterURL(rawURL string) (deckID, cardID string, err error) {
	u, perr := url.Parse(rawURL)
	if perr != nil {
		return "", "", fmt.Errorf("not a valid Hitster URL")
	}
	if u.Host == "" {
		// Physical Hitster cards' QR codes encode scheme-less URLs
		// ("www.hitstergame.com/pl/AB1/42") — without a scheme, url.Parse
		// treats the whole string as a relative path (Host stays empty), so
		// retry as if https:// were given before falling through to the
		// same host validation as a proper absolute URL.
		if u2, perr2 := url.Parse("https://" + rawURL); perr2 == nil {
			u = u2
		}
	}
	host := strings.ToLower(u.Host)
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	if host != "hitstergame.com" && !strings.HasSuffix(host, ".hitstergame.com") {
		return "", "", fmt.Errorf("not a valid Hitster URL")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 {
		return "", "", fmt.Errorf("not a valid Hitster URL")
	}
	deck, card := parts[1], parts[2]
	if !deckIDSegmentRe.MatchString(deck) || !cardIDSegmentRe.MatchString(card) {
		return "", "", fmt.Errorf("not a valid Hitster URL")
	}
	return deck, card, nil
}

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
	rawDeckID, rawCardID, err := parseHitsterURL(rawURL)
	if err != nil {
		return "", err
	}
	deckID := strings.ToLower(rawDeckID)
	cardID := rawCardID
	if n, err := strconv.Atoi(rawCardID); err == nil {
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

// load fetches and parses the full gameset database, applying it only if
// its own embedded updated_on timestamp differs from what's already
// loaded. jumboplay.com (unlike the stale host this used to point at) has
// no separate lightweight timestamp endpoint to cheaply poll first, so
// every refreshEvery tick now costs one full ~5MB fetch+parse regardless —
// entirely fine for an hourly check, and simpler than depending on a
// second endpoint that may not exist on every host this could point at.
func (r *Resolver) load() error {
	resp, err := resolverHTTPClient.Get(gamesetDB)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
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

	r.mu.RLock()
	current := r.timestamp
	r.mu.RUnlock()
	if current != 0 && db.UpdatedOn == current {
		return nil
	}

	lookup := make(map[string]string, 50000)
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
	slog.Info("gameset database loaded", "cards", len(lookup), "gamesets", len(db.Gamesets), "updated_on", db.UpdatedOn)
	return nil
}

func (r *Resolver) refreshLoop() {
	ticker := time.NewTicker(refreshEvery)
	defer ticker.Stop()
	for range ticker.C {
		if err := r.load(); err != nil {
			slog.Error("reload failed", "err", err)
		}
	}
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
