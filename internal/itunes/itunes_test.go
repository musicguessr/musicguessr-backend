package itunes

import "testing"

func TestPickEarliestMatch_PrefersOlderSameArtistResult(t *testing.T) {
	results := []resultItem{
		{ArtistName: "Dominik Hauser", TrackName: "Charlie's Angels: Theme from the TV Series", ReleaseDate: "2011-01-01T00:00:00Z"},
		{ArtistName: "Jack Elliot & Allyn Ferguson", TrackName: "Charlie's Angels Theme", ReleaseDate: "1976-01-01T00:00:00Z"},
	}
	got := pickEarliestMatch(results, "Jack Elliot")
	if got.ArtistName != "Jack Elliot & Allyn Ferguson" {
		t.Fatalf("got artist %q, want the 1976 original by Jack Elliot & Allyn Ferguson", got.ArtistName)
	}
}

func TestPickEarliestMatch_FallsBackToTopResultWhenNoArtistMatches(t *testing.T) {
	results := []resultItem{
		{ArtistName: "Some Cover Band", TrackName: "Charlie's Angels Theme", ReleaseDate: "2015-01-01T00:00:00Z"},
		{ArtistName: "Another Cover Band", TrackName: "Charlie's Angels Theme", ReleaseDate: "2005-01-01T00:00:00Z"},
	}
	got := pickEarliestMatch(results, "Jack Elliot")
	if got.ArtistName != "Some Cover Band" {
		t.Fatalf("got artist %q, want fallback to results[0] when nothing matches the query artist", got.ArtistName)
	}
}

func TestPickEarliestMatch_SingleResult(t *testing.T) {
	results := []resultItem{
		{ArtistName: "Jack Elliot", TrackName: "Charlie's Angels Theme", ReleaseDate: "1976-01-01T00:00:00Z"},
	}
	got := pickEarliestMatch(results, "Jack Elliot")
	if got.ReleaseDate != "1976-01-01T00:00:00Z" {
		t.Fatalf("got %+v, want the only result unchanged", got)
	}
}

func TestArtistMatches(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"Jack Elliot & Allyn Ferguson", "Jack Elliot", true},
		{"Jack Elliot", "Jack Elliot & Allyn Ferguson", true},
		{"jack elliot", "Jack Elliot", true},
		{"Dominik Hauser", "Jack Elliot", false},
		{"", "Jack Elliot", false},
		{"Jack Elliot", "", false},
	}
	for _, c := range cases {
		if got := artistMatches(c.a, c.b); got != c.want {
			t.Errorf("artistMatches(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestParseReleaseYear(t *testing.T) {
	cases := []struct {
		date string
		want int
	}{
		{"", 0},
		{"1976-01-01T00:00:00Z", 1976},
		{"2011-06-15T12:00:00Z", 2011},
		{"not-a-date", 0},
	}
	for _, c := range cases {
		if got := parseReleaseYear(c.date); got != c.want {
			t.Errorf("parseReleaseYear(%q) = %d, want %d", c.date, got, c.want)
		}
	}
}
