package youtube

import (
	"context"
	"testing"
)

type fakeCache struct {
	m map[string]string
}

func newFakeCache() *fakeCache { return &fakeCache{m: map[string]string{}} }

func (f *fakeCache) Get(key string) (string, bool) {
	v, ok := f.m[key]
	return v, ok
}

func (f *fakeCache) Set(key, videoID string) {
	f.m[key] = videoID
}

func TestSearchVideoID_CacheHitSkipsSearch(t *testing.T) {
	t.Cleanup(func() { SetCache(nil) })

	fc := newFakeCache()
	key := searchCacheKey("Daft Punk", "Get Lucky", false)
	fc.m[key] = "cached-video-id"
	SetCache(fc)

	// If this reaches ytSearch (real yt-dlp subprocess), it will either hang
	// on a missing interpreter or take real network time — the test's point
	// is that a cache hit returns immediately without doing either.
	id, err := SearchVideoID(context.Background(), "Daft Punk", "Get Lucky", false)
	if err != nil {
		t.Fatalf("expected cache hit, got error: %v", err)
	}
	if id != "cached-video-id" {
		t.Errorf("got %q, want %q", id, "cached-video-id")
	}
}

func TestSearchCacheKey_VariantsDontCollide(t *testing.T) {
	strict := searchCacheKey("Queen", "Bohemian Rhapsody", false)
	relaxed := searchCacheKey("Queen", "Bohemian Rhapsody", true)
	if strict == relaxed {
		t.Error("strict and variants-allowed cache keys must differ")
	}
}

func TestSearchCacheKey_NormalizesCase(t *testing.T) {
	a := searchCacheKey("Daft Punk", "Get Lucky", false)
	b := searchCacheKey("DAFT PUNK", "get lucky", false)
	if a != b {
		t.Errorf("expected case-insensitive keys to match: %q != %q", a, b)
	}
}

func TestCoreTitle(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Maczo (Dub)", "Maczo"},
		{"Wake Me Up (feat. Aloe Blacc)", "Wake Me Up"},
		{"Loca Bambina - Radio edit", "Loca Bambina"},
		{"Blinding Lights [Remix]", "Blinding Lights"},
		{"99 Luftballons", "99 Luftballons"},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := coreTitle(tc.in)
			if got != tc.want {
				t.Errorf("coreTitle(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Daft Punk", "daft punk"},
		{"99 Luftballons", "99 luftballons"},
		{"Łódź", "lodz"},
		{"café", "cafe"},
		{"  multiple   spaces  ", "multiple spaces"},
		{"AC/DC", "ac dc"},
		{"Hello, World!", "hello world"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := normalize(tc.in)
			if got != tc.want {
				t.Errorf("normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFilterComments(t *testing.T) {
	in := []string{"live", "// this is a comment", "remix", "", "  cover  ", "//another"}
	got := filterComments(in)
	want := []string{"live", "remix", "cover"}

	if len(got) != len(want) {
		t.Fatalf("filterComments: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("filterComments[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMeaningfulWords(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"the quick brown fox", []string{"the", "quick", "brown", "fox"}},
		{"hello world", []string{"hello", "world"}},
		// words with ≤ 2 runes are excluded
		{"in a by", nil},
		{"up on it", nil},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := meaningfulWords(tc.in)
			if len(got) != len(tc.want) {
				t.Errorf("meaningfulWords(%q) = %v, want %v", tc.in, got, tc.want)
				return
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("meaningfulWords(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestWordSet(t *testing.T) {
	set := wordSet("hello world hello")
	if !set["hello"] {
		t.Error("expected 'hello' in word set")
	}
	if !set["world"] {
		t.Error("expected 'world' in word set")
	}
	if set["missing"] {
		t.Error("'missing' should not be in word set")
	}
}

func TestScoreMatch_LiveVariantDisqualified(t *testing.T) {
	// "live" is an unwanted variant; "Bohemian Rhapsody" and "Queen" don't contain it
	strict := scoreMatch("Bohemian Rhapsody Live at Wembley", "Queen", "Bohemian Rhapsody", false)
	if strict != 0 {
		t.Errorf("expected 0 for live variant in strict mode, got %d", strict)
	}

	relaxed := scoreMatch("Bohemian Rhapsody Live at Wembley", "Queen", "Bohemian Rhapsody", true)
	if relaxed == 0 {
		t.Errorf("expected non-zero score in relaxed mode, got 0")
	}
}

func TestScoreMatch_RemixDisqualified(t *testing.T) {
	// "remix" is an unwanted variant
	score := scoreMatch("Get Lucky Remix Daft Punk", "Daft Punk", "Get Lucky", false)
	if score != 0 {
		t.Errorf("expected 0 for remix in strict mode, got %d", score)
	}
}

func TestScoreMatch_OfficialBonus(t *testing.T) {
	// "official" and "audio" are both official_markers — no artist in title to avoid interference
	withBonus := scoreMatch("Get Lucky Official Audio", "Daft Punk", "Get Lucky", false)
	withoutBonus := scoreMatch("Get Lucky", "Daft Punk", "Get Lucky", false)
	if withBonus <= withoutBonus {
		t.Errorf("official marker bonus not applied: with=%d, without=%d", withBonus, withoutBonus)
	}
}

func TestScoreMatch_ArtistBonus(t *testing.T) {
	withArtist := scoreMatch("Blinding Lights The Weeknd Official", "The Weeknd", "Blinding Lights", false)
	withoutArtist := scoreMatch("Blinding Lights Official", "The Weeknd", "Blinding Lights", false)
	if withArtist <= withoutArtist {
		t.Errorf("artist match bonus not applied: with=%d, without=%d", withArtist, withoutArtist)
	}
}

func TestScoreMatch_TitleCoverage(t *testing.T) {
	// Full title word coverage → score ≥ 7
	high := scoreMatch("Blinding Lights The Weeknd Official", "The Weeknd", "Blinding Lights", false)
	if high < 7 {
		t.Errorf("expected score ≥ 7 for full title match, got %d", high)
	}

	// No title words in video → score = 0
	zero := scoreMatch("Completely Unrelated Song Title", "The Weeknd", "Blinding Lights", false)
	if zero >= 7 {
		t.Errorf("expected score < 7 for unrelated video, got %d", zero)
	}
}

// Real lookups that failed in production ("no confident match found") even
// though the right upload was in yt-dlp's results: each is the exact Spotify
// title and the video title YouTube returned.
func TestScoreMatch_TitleVariationsSeenInProduction(t *testing.T) {
	tests := []struct {
		name         string
		video        string
		artist       string
		title        string
		wantMatch    bool
		wantStrictly bool // must also pass the strict (original-only) pass
	}{
		{"dotted acronym", "Justice - D.A.N.C.E. (Official Video)", "Justice", "D.A.N.C.E.", true, true},
		{"dotted acronym, no trailing dot", "Justice - D.A.N.C.E", "Justice", "D.A.N.C.E.", true, true},
		{"elision written differently", "Yves Montand - Moi j'm'en fous", "Yves Montand", "Moi, je m'en fous", true, true},
		{"elision, same spelling", "Yves Montand - Moi, je m'en fous", "Yves Montand", "Moi, je m'en fous", true, true},
		{"curly vs straight apostrophe", "Lara Fabian - Je t'aime", "Lara Fabian", "Je T’aime", true, true},
		{"straight vs curly apostrophe", "Lara Fabian - Je t’aime", "Lara Fabian", "Je T'aime", true, true},
		{"live upload still rejected", "Lara Fabian - Je t'aime - Live in Paris, 2001", "Lara Fabian", "Je T’aime", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			strict := scoreMatch(tc.video, tc.artist, tc.title, false)
			relaxed := scoreMatch(tc.video, tc.artist, tc.title, true)
			if tc.wantStrictly && strict < 7 {
				t.Errorf("strict score = %d, want >= 7", strict)
			}
			if !tc.wantStrictly && strict != 0 {
				t.Errorf("strict score = %d, want 0 (variant must stay disqualified)", strict)
			}
			if tc.wantMatch && relaxed < 7 {
				t.Errorf("relaxed score = %d, want >= 7", relaxed)
			}
		})
	}
}

// The folding must not turn unrelated titles into matches.
func TestScoreMatch_FoldingDoesNotCreateFalseMatches(t *testing.T) {
	tests := []struct{ video, artist, title string }{
		{"Justice - Fire (Official Video)", "Justice", "D.A.N.C.E."},
		{"Yves Montand - Les feuilles mortes", "Yves Montand", "Moi, je m'en fous"},
		{"Lara Fabian - Adagio", "Lara Fabian", "Je T’aime"},
	}
	for _, tc := range tests {
		if got := scoreMatch(tc.video, tc.artist, tc.title, true); got >= 7 {
			t.Errorf("scoreMatch(%q, %q, %q) = %d, want < 7", tc.video, tc.artist, tc.title, got)
		}
	}
}

func TestNormalizeForMatch(t *testing.T) {
	tests := []struct{ in, want string }{
		{"D.A.N.C.E.", "dance"},
		{"R.E.M.", "rem"},
		{"Je T’aime", "je aime"},
		{"Moi j'm'en fous", "moi jen fous"},
		{"L'Impératrice", "imperatrice"},
		{"I'm Good", "im good"},     // not an elision: unchanged from normalize()
		{"Don’t Stop", "dont stop"}, // curly quote folded, contraction kept
		{"feat. Jul", "feat jul"},   // a dot after a word is not an acronym
	}
	for _, tc := range tests {
		if got := normalizeForMatch(tc.in); got != tc.want {
			t.Errorf("normalizeForMatch(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Cache keys must keep using the plain normalize(), or every cached lookup
// with an apostrophe or dotted acronym in its title would be orphaned.
func TestSearchCacheKey_UnchangedByMatchFolding(t *testing.T) {
	if got, want := searchCacheKey("Justice", "D.A.N.C.E.", true), "justice|d a n c e|1"; got != want {
		t.Errorf("searchCacheKey = %q, want %q", got, want)
	}
}
