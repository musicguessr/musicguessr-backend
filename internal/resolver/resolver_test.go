package resolver

import (
	"strings"
	"testing"
)

func TestSpotifyURL(t *testing.T) {
	got := SpotifyURL("abc123")
	want := "https://open.spotify.com/track/abc123"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStreamingLinks(t *testing.T) {
	links := StreamingLinks("Daft Punk", "Get Lucky")

	requiredKeys := []string{"youtube_music", "youtube", "tidal", "deezer"}
	for _, k := range requiredKeys {
		if _, ok := links[k]; !ok {
			t.Errorf("missing key %q in streaming links", k)
		}
	}

	for k, v := range links {
		if !strings.Contains(v, "Daft") {
			t.Errorf("links[%q] = %q: expected artist name to appear in URL", k, v)
		}
	}
}

func TestResolve(t *testing.T) {
	r := &Resolver{lookup: map[string]string{
		"testdeck:00042": "spotify_track_abc",
		"testdeck:00001": "spotify_track_xyz",
	}}

	tests := []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		{
			name: "numeric card ID padded to 5 digits",
			url:  "https://hitstergame.com/en/TESTDECK/42",
			want: "spotify_track_abc",
		},
		{
			name: "card ID 1 padded to 00001",
			url:  "https://hitstergame.com/pl/TESTDECK/1",
			want: "spotify_track_xyz",
		},
		{
			name: "deck ID is case-insensitive",
			url:  "https://hitstergame.com/en/testdeck/42",
			want: "spotify_track_abc",
		},
		{
			name:    "card not in lookup",
			url:     "https://hitstergame.com/en/TESTDECK/99",
			wantErr: true,
		},
		{
			name:    "invalid URL pattern",
			url:     "https://example.com/not-a-hitster-url",
			wantErr: true,
		},
		{
			name:    "empty string",
			url:     "",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Resolve(tc.url)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
