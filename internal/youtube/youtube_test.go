package youtube

import (
	"testing"
)

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

func TestInstances_Default(t *testing.T) {
	t.Setenv("INVIDIOUS_INSTANCES", "")
	got := Instances()
	if len(got) == 0 {
		t.Error("expected default instances when env var is empty")
	}
}

func TestInstances_FromEnv(t *testing.T) {
	t.Setenv("INVIDIOUS_INSTANCES", "https://a.example.com, https://b.example.com")
	got := Instances()
	if len(got) != 2 {
		t.Fatalf("expected 2 instances, got %d: %v", len(got), got)
	}
	if got[0] != "https://a.example.com" {
		t.Errorf("got[0] = %q, want %q", got[0], "https://a.example.com")
	}
	if got[1] != "https://b.example.com" {
		t.Errorf("got[1] = %q, want %q", got[1], "https://b.example.com")
	}
}
