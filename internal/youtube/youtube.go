package youtube

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// UserAgent is sent on every outbound request to Invidious instances — a
// single source of truth so it can't drift between call sites.
const UserAgent = "musicguessr/1.0 (+https://github.com/musicguessr)"

// ErrNotFound is returned by FetchJSON when an Invidious instance gives a
// confirmed 404 for the requested resource — a definitive negative that
// should stop the failover loop immediately, distinct from every instance
// being unreachable/erroring (ErrAllInstancesFailed).
var ErrNotFound = errors.New("youtube: resource not found")

// ErrAllInstancesFailed is returned by FetchJSON when every configured
// Invidious instance failed for reasons other than a confirmed 404
// (network error, non-200/404 status, bad JSON) — an infrastructure
// problem, not evidence the requested resource doesn't exist.
var ErrAllInstancesFailed = errors.New("youtube: all invidious instances failed")

//go:embed filters.json
var filtersJSON []byte

type filtersFile struct {
	UnwantedVariants []string `json:"unwanted_variants"`
	MixWordSuffixes  []string `json:"mix_word_suffixes"`
	OfficialMarkers  []string `json:"official_markers"`
}

var defaultInstances = []string{
	"https://iv.melmac.space",
	"https://invidious.darkness.services",
}

var client = &http.Client{
	Timeout: 6 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     30 * time.Second,
	},
}

// Instances returns the list of Invidious instances to use, from INVIDIOUS_INSTANCES env var or defaults.
func Instances() []string {
	env := os.Getenv("INVIDIOUS_INSTANCES")
	if env == "" {
		return defaultInstances
	}
	parts := strings.Split(env, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	// A set env var that reduces to zero usable entries after trim/split
	// (a blank string, a comma-only value) is almost certainly a deploy
	// misconfiguration, not an intentional "use no instances" — fall back to
	// the defaults rather than making every YouTube-dependent endpoint fail
	// uniformly with no signal pointing at the actual cause.
	if len(out) == 0 {
		slog.Warn("INVIDIOUS_INSTANCES set but yielded no usable entries, falling back to defaults", "raw", env)
		return defaultInstances
	}
	return out
}

// FetchJSON tries each configured Invidious instance in turn — buildURL(instance)
// builds the request URL for that instance — decoding the first 200 response's
// JSON body into out. A confirmed 404 from any instance stops the loop
// immediately and returns ErrNotFound (trying other instances wouldn't turn a
// real "not found" into a "found"); any other per-instance failure (network
// error, non-200 status, bad JSON) is logged and the next instance is tried.
// If every instance fails that way, ErrAllInstancesFailed is returned.
func FetchJSON(ctx context.Context, buildURL func(instance string) string, out any) error {
	instances := Instances()
	for _, inst := range instances {
		reqURL := buildURL(inst)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			slog.Warn("invidious request build failed", "instance", inst, "err", err)
			continue
		}
		req.Header.Set("User-Agent", UserAgent)
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			slog.Warn("invidious instance request failed", "instance", inst, "err", err)
			continue
		}
		if resp.StatusCode == http.StatusNotFound {
			_ = resp.Body.Close()
			return ErrNotFound
		}
		if resp.StatusCode != http.StatusOK {
			slog.Warn("invidious instance bad status", "instance", inst, "status", resp.StatusCode)
			_ = resp.Body.Close()
			continue
		}
		err = json.NewDecoder(resp.Body).Decode(out)
		_ = resp.Body.Close()
		if err != nil {
			slog.Warn("invidious instance decode failed", "instance", inst, "err", err)
			continue
		}
		return nil
	}
	return ErrAllInstancesFailed
}

type invidiousItem struct {
	Type    string `json:"type"`
	VideoID string `json:"videoId"`
	Title   string `json:"title"`
}

// SearchVideoID finds the best YouTube video ID for a track.
// If allowVariants is true and no original is found, a second pass is attempted
// that accepts remixes, acoustic versions, and other alt versions as a fallback.
func SearchVideoID(ctx context.Context, artist, title string, allowVariants bool) (string, error) {
	// Use ASCII-normalized coreTitle for the query:
	// - removes diacritics so Polish/French/German chars don't confuse Invidious instances
	// - strips parenthetical version info ("(Radio edit)", "(feat. X)") for better recall
	// - removes commas and brackets that can break query parsing
	q := url.QueryEscape(normalizeForQuery(artist) + " " + normalizeForQuery(coreTitle(title)))
	slog.Info("youtube search start", "artist", artist, "title", title, "allowVariants", allowVariants)
	for _, inst := range Instances() {
		id, score, variant, err := tryInstance(ctx, inst, q, artist, title, allowVariants)
		if err != nil {
			slog.Warn("invidious instance failed", "instance", inst, "err", err)
			continue
		}
		if variant {
			slog.Info("youtube matched variant fallback", "instance", inst, "videoId", id, "score", score)
		} else {
			slog.Info("youtube search success", "instance", inst, "videoId", id, "score", score)
		}
		return id, nil
	}
	return "", fmt.Errorf("no confident match found for %q – %q", artist, title)
}

func tryInstance(ctx context.Context, instance, query, artist, title string, allowVariants bool) (string, int, bool, error) {
	reqURL := instance + "/api/v1/search?q=" + query + "&type=video&fields=videoId,title,type"
	slog.Debug("invidious request", "url", reqURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", 0, false, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", 0, false, fmt.Errorf("status %d", resp.StatusCode)
	}
	var items []invidiousItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return "", 0, false, err
	}

	// Pass 1: strict — original only, no remixes/variants
	id, score := bestMatch(items, artist, title, false)
	for i, item := range items {
		slog.Debug("invidious item", "index", i, "videoId", item.VideoID, "title", item.Title, "score", scoreMatch(item.Title, artist, title, false))
	}
	if score >= 7 {
		return id, score, false, nil
	}

	// Pass 2: relaxed — allow remixes/alt versions as fallback
	if allowVariants {
		id, score = bestMatch(items, artist, title, true)
		if score >= 7 {
			slog.Debug("youtube variant fallback pass", "videoId", id, "score", score)
			return id, score, true, nil
		}
	}

	return "", score, false, fmt.Errorf("no confident match (best score %d) for %q – %q", score, artist, title)
}

// bestMatch returns the video with the highest confidence score.
// relaxed=true skips variant disqualification (remix, acoustic, etc.) for fallback matching.
func bestMatch(items []invidiousItem, artist, title string, relaxed bool) (string, int) {
	bestID := ""
	bestScore := 0
	for _, item := range items {
		if item.Type != "video" || item.VideoID == "" {
			continue
		}
		s := scoreMatch(item.Title, artist, title, relaxed)
		if s > bestScore {
			bestScore = s
			bestID = item.VideoID
		}
	}
	return bestID, bestScore
}

// filters are loaded from filters.json at init time via go:embed.
var (
	unwantedVariants []string
	mixWordSuffixes  []string
	officialMarkers  []string
)

func init() {
	var f filtersFile
	if err := json.Unmarshal(filtersJSON, &f); err != nil {
		panic("youtube: failed to parse filters.json: " + err.Error())
	}
	// Strip comment entries (start with "//") and empty strings.
	unwantedVariants = filterComments(f.UnwantedVariants)
	mixWordSuffixes = filterComments(f.MixWordSuffixes)
	officialMarkers = filterComments(f.OfficialMarkers)
	slog.Debug("youtube filters loaded",
		"unwanted", len(unwantedVariants),
		"mix_suffixes", len(mixWordSuffixes),
		"official", len(officialMarkers),
	)
}

// filterComments removes comment entries (starting with "//") and blank strings.
func filterComments(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if s == "" || strings.HasPrefix(s, "//") {
			continue
		}
		out = append(out, s)
	}
	return out
}

// coreTitle strips trailing version/feature suffixes from a track title so that
// word-coverage scoring only requires the core song name, not variant metadata.
//
// Examples:
//
//	"Maczo (Dub)"              → "Maczo"
//	"Wake Me Up (feat. ...)"   → "Wake Me Up"
//	"Loca Bambina - Radio edit"→ "Loca Bambina"
//	"99 Luftballons"           → "99 Luftballons"   (unchanged)
func coreTitle(s string) string {
	// Strip "(X)" / "[X]" suffix first — most common in Spotify titles
	if idx := strings.Index(s, " ("); idx > 0 {
		return s[:idx]
	}
	if idx := strings.Index(s, " ["); idx > 0 {
		return s[:idx]
	}
	// Strip " - X" suffix — used when Spotify encodes version info without parens
	// e.g. "Loca Bambina - Radio edit", "Song Title - Remastered"
	// Spotify track titles rarely contain " - " as part of the real name,
	// so this is a safe heuristic.
	if idx := strings.Index(s, " - "); idx > 0 {
		return s[:idx]
	}
	return s
}

func scoreMatch(videoTitle, artist, title string, relaxed bool) int {
	normVid := normalize(videoTitle)
	normTitle := normalize(title)
	normArtist := normalize(artist)
	normCore := normalize(coreTitle(title))

	if !relaxed {
		// Disqualify non-original versions unless the original title/artist itself contains the marker
		// (e.g. "Live and Let Die" legitimately contains "live"; "Cover Me" contains "cover")
		for _, v := range unwantedVariants {
			if strings.Contains(normVid, v) &&
				!strings.Contains(normTitle, v) &&
				!strings.Contains(normArtist, v) {
				slog.Debug("youtube disqualified non-original", "video", videoTitle, "marker", v)
				return 0
			}
		}

		// Disqualify video titles that contain a word ending with a mix suffix
		// (catches "NoMexMix", "DubMix", "LoungeMix", etc.) unless the original has it too.
		for _, w := range strings.Fields(normVid) {
			for _, sfx := range mixWordSuffixes {
				if strings.HasSuffix(w, sfx) &&
					!strings.Contains(normTitle, w) &&
					!strings.Contains(normArtist, w) {
					slog.Debug("youtube disqualified mix-variant", "video", videoTitle, "word", w)
					return 0
				}
			}
		}
	}

	vidWords := wordSet(normVid)
	// Use core title (without parentheticals) for required word coverage.
	// Parenthetical content like "(Dub)" or "(feat. X)" is version metadata — the video may
	// legitimately omit it while still being the correct original upload.
	titleWords := meaningfulWords(normCore)
	artistWords := meaningfulWords(normArtist)

	if len(titleWords) == 0 {
		return 0
	}

	matched := 0
	for _, w := range titleWords {
		if vidWords[w] {
			matched++
		}
	}

	ratio := float64(matched) / float64(len(titleWords))
	if ratio < 0.7 {
		return 0
	}
	score := int(ratio * 10) // 7–10

	// Bonus for artist match
	for _, w := range artistWords {
		if vidWords[w] {
			score += 2
			break
		}
	}

	// Bonus for official upload markers — each matching marker adds +1 (max +3 total).
	// e.g. "Official Music Video" → +2, "Oficjalny Teledysk" → +2, "Official Audio" → +2
	officialBonus := 0
	for _, m := range officialMarkers {
		if strings.Contains(normVid, m) {
			officialBonus++
		}
	}
	if officialBonus > 3 {
		officialBonus = 3
	}
	score += officialBonus

	return score
}

func wordSet(s string) map[string]bool {
	set := make(map[string]bool)
	for _, w := range strings.Fields(s) {
		set[w] = true
	}
	return set
}

// meaningfulWords returns words longer than 2 characters (skip "w", "to", "in", "a", etc.)
func meaningfulWords(s string) []string {
	var out []string
	for _, w := range strings.Fields(s) {
		if len([]rune(w)) > 2 {
			out = append(out, w)
		}
	}
	return out
}

var diacritics = strings.NewReplacer(
	"ą", "a", "ć", "c", "ę", "e", "ł", "l", "ń", "n", "ó", "o", "ś", "s", "ź", "z", "ż", "z",
	"à", "a", "á", "a", "â", "a", "ä", "a", "ã", "a",
	"è", "e", "é", "e", "ê", "e", "ë", "e",
	"ì", "i", "í", "i", "î", "i", "ï", "i",
	"ò", "o", "ô", "o", "ö", "o", "õ", "o",
	"ù", "u", "ú", "u", "û", "u", "ü", "u",
	"ñ", "n", "ç", "c", "ß", "ss",
)

var punct = strings.NewReplacer(
	"-", " ", "_", " ", ".", " ", ",", " ", ":", " ", ";", " ",
	"(", " ", ")", " ", "[", " ", "]", " ", "{", " ", "}", " ",
	"'", "", "'", "", "\"", "", "!", "", "?", "", "&", " ",
	"/", " ", "\\", " ", "|", " ", "+", " ", "=", " ",
)

func normalize(s string) string {
	s = strings.ToLower(s)
	s = diacritics.Replace(s)
	s = punct.Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// normalizeForQuery prepares a string for use as an Invidious/YouTube search query.
// It removes diacritics and search-breaking punctuation (commas, brackets, etc.)
// but preserves word spacing so the query remains readable by the search engine.
func normalizeForQuery(s string) string {
	s = strings.ToLower(s)
	s = diacritics.Replace(s)
	s = punct.Replace(s)
	return strings.Join(strings.Fields(s), " ")
}
