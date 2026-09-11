package rcache

import (
	"context"
	"testing"
)

type trackLike struct {
	Artist string `json:"artist"`
	Year   int    `json:"year"`
}

func TestCache_S3OnlyRoundTrip(t *testing.T) {
	t.Setenv("RESOLVE_CACHE_PROVIDER", "memory")
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !c.Enabled() {
		t.Fatal("expected cache to be enabled with RESOLVE_CACHE_PROVIDER=memory")
	}

	ctx := context.Background()
	var out trackLike
	if ok := c.GetJSON(ctx, "youtube", "daft punk|get lucky", &out); ok {
		t.Fatal("expected miss on empty cache")
	}

	in := trackLike{Artist: "Daft Punk", Year: 2013}
	c.SetJSON(ctx, "youtube", "daft punk|get lucky", in)

	if ok := c.GetJSON(ctx, "youtube", "daft punk|get lucky", &out); !ok {
		t.Fatal("expected hit after SetJSON")
	}
	if out != in {
		t.Errorf("got %+v, want %+v", out, in)
	}
}

func TestCache_DisabledByDefault(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Enabled() {
		t.Fatal("expected cache to be disabled with no VALKEY_ADDR/RESOLVE_CACHE_PROVIDER set")
	}

	ctx := context.Background()
	c.SetJSON(ctx, "youtube", "k", trackLike{Artist: "x"})
	var out trackLike
	if ok := c.GetJSON(ctx, "youtube", "k", &out); ok {
		t.Fatal("expected disabled cache to never report a hit")
	}
}

func TestCache_NamespacesDontCollide(t *testing.T) {
	t.Setenv("RESOLVE_CACHE_PROVIDER", "memory")
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	c.SetJSON(ctx, "youtube", "same-key", trackLike{Artist: "yt"})
	c.SetJSON(ctx, "metadata", "same-key", trackLike{Artist: "meta"})

	var out trackLike
	if ok := c.GetJSON(ctx, "youtube", "same-key", &out); !ok || out.Artist != "yt" {
		t.Errorf("youtube namespace got %+v, ok=%v", out, ok)
	}
	if ok := c.GetJSON(ctx, "metadata", "same-key", &out); !ok || out.Artist != "meta" {
		t.Errorf("metadata namespace got %+v, ok=%v", out, ok)
	}
}

func TestCache_NilSafe(t *testing.T) {
	var c *Cache
	ctx := context.Background()
	if c.Enabled() {
		t.Fatal("nil cache must report disabled")
	}
	c.SetJSON(ctx, "ns", "k", trackLike{}) // must not panic
	var out trackLike
	if ok := c.GetJSON(ctx, "ns", "k", &out); ok {
		t.Fatal("nil cache must never hit")
	}
}
