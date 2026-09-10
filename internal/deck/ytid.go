package deck

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var ytIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{11}$`)

// isYouTubeHost reports whether host is youtube.com (or a subdomain of it) or
// youtu.be — anchored so "evilyoutube.com" or "youtube.com.evil.tld" don't match.
func isYouTubeHost(host string) bool {
	host = strings.ToLower(host)
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	return host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") || host == "youtu.be"
}

// extractYtID returns the 11-character YouTube video ID from a URL or bare ID.
func extractYtID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if ytIDRe.MatchString(raw) {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil || !isYouTubeHost(u.Host) {
		return "", fmt.Errorf("not a youtube.com or youtu.be url")
	}
	// youtu.be/{id}
	if u.Host == "youtu.be" || strings.HasSuffix(strings.ToLower(u.Host), ".youtu.be") {
		id := strings.TrimPrefix(u.Path, "/")
		if ytIDRe.MatchString(id) {
			return id, nil
		}
	}
	// youtube.com/watch?v={id}
	if v := u.Query().Get("v"); ytIDRe.MatchString(v) {
		return v, nil
	}
	// youtube.com/shorts/{id} or /embed/{id} or /live/{id}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, p := range parts {
		if (p == "shorts" || p == "embed" || p == "live" || p == "v") && i+1 < len(parts) {
			if ytIDRe.MatchString(parts[i+1]) {
				return parts[i+1], nil
			}
		}
	}
	return "", fmt.Errorf("could not extract youtube video id from %q", raw)
}
