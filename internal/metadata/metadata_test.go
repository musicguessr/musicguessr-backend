package metadata

import (
	"testing"
	"time"

	"github.com/musicguessr/musicguessr-backend/internal/itunes"
)

func TestNormalizeKey(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"  Hello World  ", "hello world"},
		{"Taylor Swift", "taylor swift"},
		{"multiple   spaces", "multiple spaces"},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := normalizeKey(tc.in)
			if got != tc.want {
				t.Errorf("normalizeKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestChooseMostCommon(t *testing.T) {
	t.Run("majority winner", func(t *testing.T) {
		got := chooseMostCommon([]string{"daft punk", "daft punk", "something else"})
		if got != "daft punk" {
			t.Errorf("got %q, want %q", got, "daft punk")
		}
	})
	t.Run("single element", func(t *testing.T) {
		got := chooseMostCommon([]string{"only"})
		if got != "only" {
			t.Errorf("got %q, want %q", got, "only")
		}
	})
	t.Run("empty", func(t *testing.T) {
		got := chooseMostCommon(nil)
		if got != "" {
			t.Errorf("got %q, want empty string", got)
		}
	})
}

func TestChooseMostCommonInt(t *testing.T) {
	t.Run("majority winner", func(t *testing.T) {
		got := chooseMostCommonInt([]int{1995, 1997, 1995})
		if got != 1995 {
			t.Errorf("got %d, want 1995", got)
		}
	})
	t.Run("empty", func(t *testing.T) {
		got := chooseMostCommonInt(nil)
		if got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})
	t.Run("tiebreaker prefers higher value", func(t *testing.T) {
		got := chooseMostCommonInt([]int{1995, 2000})
		if got != 2000 {
			t.Errorf("got %d, want 2000", got)
		}
	})
}

func TestChooseArtworkPreferred(t *testing.T) {
	tests := []struct {
		name string
		urls []string
		want string
	}{
		{
			name: "coverartarchive over all others",
			urls: []string{
				"https://img.deezer.com/cover.jpg",
				"https://coverartarchive.org/release/abc/front",
			},
			want: "https://coverartarchive.org/release/abc/front",
		},
		{
			name: "discogs over deezer",
			urls: []string{
				"https://img.deezer.com/cover_big.jpg",
				"https://img.discogs.com/thumb.jpg",
			},
			want: "https://img.discogs.com/thumb.jpg",
		},
		{
			name: "fallback to first element",
			urls: []string{"https://example.com/art.jpg"},
			want: "https://example.com/art.jpg",
		},
		{
			name: "empty slice",
			urls: nil,
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := chooseArtworkPreferred(tc.urls)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "", "third", "fourth"); got != "third" {
		t.Errorf("got %q, want %q", got, "third")
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Errorf("got %q, want empty string", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestMemoryCache_HitAndMiss(t *testing.T) {
	c := NewMemoryCache(time.Minute)
	track := &itunes.Track{Artist: "Daft Punk", Title: "Get Lucky", Year: 2013}

	// cache miss
	if _, ok := c.Get("key1"); ok {
		t.Error("expected cache miss before Set, got hit")
	}

	// cache hit after Set
	c.Set("key1", track, 0)
	got, ok := c.Get("key1")
	if !ok {
		t.Fatal("expected cache hit after Set, got miss")
	}
	if got.Artist != "Daft Punk" {
		t.Errorf("Artist: got %q, want %q", got.Artist, "Daft Punk")
	}
	if got.Year != 2013 {
		t.Errorf("Year: got %d, want 2013", got.Year)
	}
}

func TestMemoryCache_TTLExpiry(t *testing.T) {
	c := NewMemoryCache(time.Hour)
	track := &itunes.Track{Artist: "Test"}

	c.Set("expiring", track, time.Millisecond)
	time.Sleep(10 * time.Millisecond)

	if _, ok := c.Get("expiring"); ok {
		t.Error("expected cache miss after TTL expired, got hit")
	}
}
