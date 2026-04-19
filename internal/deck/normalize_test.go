package deck

import "testing"

func TestNormalizeYTAuthor(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Taylor Swift - Topic", "Taylor Swift"},
		{"TaylorSwiftVEVO", "TaylorSwift"},
		{"Ed Sheeran Official", "Ed Sheeran"},
		{"Ed Sheeran - Official Channel", "Ed Sheeran"},
		{"Sony Music", "Sony"},
		{"Warner Records", "Warner"},
		{"Plain Artist Name", "Plain Artist Name"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := normalizeYTAuthor(tc.in)
			if got != tc.want {
				t.Errorf("normalizeYTAuthor(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeYTTitle(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Shape of You (Official Music Video)", "Shape of You"},
		{"Blinding Lights [Official Audio]", "Blinding Lights"},
		{"Levitating ft. DaBaby", "Levitating"},
		{"Song Title feat. Artist Name", "Song Title"},
		{"Song Title featuring Artist", "Song Title"},
		{"Song (Remastered 2023)", "Song"},
		{"Song [HD]", "Song"},
		{"Simple Title", "Simple Title"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := normalizeYTTitle(tc.in)
			if got != tc.want {
				t.Errorf("normalizeYTTitle(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
