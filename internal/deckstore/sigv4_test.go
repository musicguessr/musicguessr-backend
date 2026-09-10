package deckstore

import (
	"net/http"
	"testing"
)

func newTestRequest(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("http.NewRequest(%q): %v", rawURL, err)
	}
	return req
}

func TestAwsQueryEscape(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"list-type", "list-type"},
		{"a b", "a%20b"}, // space must be %20, not '+'
		{"a+b", "a%2Bb"}, // literal '+' must itself be escaped
		{"a=b", "a%3Db"},
		{"AZaz09-_.~", "AZaz09-_.~"},
	}
	for _, tc := range tests {
		if got := awsQueryEscape(tc.in); got != tc.want {
			t.Errorf("awsQueryEscape(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildCanonicalQuery_SortsKeysAndRepeatedValues(t *testing.T) {
	req := newTestRequest(t, "https://example.com/?b=2&a=z&a=a&c=hello%20world")
	got := buildCanonicalQuery(req)
	want := "a=a&a=z&b=2&c=hello%20world"
	if got != want {
		t.Errorf("buildCanonicalQuery() = %q, want %q", got, want)
	}
}

func TestBuildCanonicalQuery_Empty(t *testing.T) {
	req := newTestRequest(t, "https://example.com/")
	if got := buildCanonicalQuery(req); got != "" {
		t.Errorf("buildCanonicalQuery() = %q, want empty", got)
	}
}
