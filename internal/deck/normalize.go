package deck

import (
	"regexp"
	"strings"
)

var (
	// Parenthesized decorations in YT titles, e.g. "(Official Video)", "(Remastered 2023)"
	ytParenRe = regexp.MustCompile(`(?i)\s*\([^)]*\b(official|lyric|video|audio|music|live|version|remaster(?:ed)?|hd|hq|mv|4k|visualizer|clip)\b[^)]*\)`)
	// Bracketed variants, e.g. "[Official Music Video]"
	ytBracketRe = regexp.MustCompile(`(?i)\s*\[[^\]]*\b(official|lyric|video|audio|music|live|version|remaster(?:ed)?|hd|hq|mv|4k|visualizer|clip)\b[^\]]*\]`)
	// Featuring section: "ft. X", "feat. X", "featuring X"
	ytFeatRe = regexp.MustCompile(`(?i)\s+(?:ft\.?|feat\.?|featuring)\s+.+$`)
)

// normalizeYTAuthor strips YouTube channel name artifacts to get a cleaner artist name.
//
//	"Taylor Swift - Topic" → "Taylor Swift"
//	"TaylorSwiftVEVO"      → "TaylorSwift"
//	"Ed Sheeran Official"  → "Ed Sheeran"
func normalizeYTAuthor(s string) string {
	// Auto-generated music topic channels: "Artist - Topic"
	if i := strings.LastIndex(s, " - Topic"); i != -1 {
		s = s[:i]
	}
	// VEVO suffix (no space, e.g. "TaylorSwiftVEVO")
	s = strings.TrimSuffix(s, "VEVO")
	// Common channel suffixes — try longest match first
	for _, sfx := range []string{
		" - Official Channel", " Official Channel",
		" - Official Music", " Official Music",
		" - Official", " Official",
		" Music", " Records", " Channel", " TV",
	} {
		if strings.HasSuffix(s, sfx) {
			s = s[:len(s)-len(sfx)]
			break
		}
	}
	return strings.TrimSpace(s)
}

// normalizeYTTitle strips common YouTube title decorations to get a cleaner track name.
//
//	"Shape of You (Official Music Video)" → "Shape of You"
//	"Blinding Lights [Official Audio]"    → "Blinding Lights"
//	"Levitating ft. DaBaby"               → "Levitating"
func normalizeYTTitle(s string) string {
	clean := ytParenRe.ReplaceAllString(s, "")
	clean = ytBracketRe.ReplaceAllString(clean, "")
	clean = ytFeatRe.ReplaceAllString(clean, "")
	return strings.TrimSpace(clean)
}
