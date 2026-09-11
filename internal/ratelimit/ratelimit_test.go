package ratelimit

import (
	"testing"
	"time"
)

func TestAllowWithinBurst(t *testing.T) {
	l := New(1, 3, time.Minute)
	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatalf("request %d: expected allow within burst", i)
		}
	}
	if l.Allow("a") {
		t.Fatal("expected 4th request to be denied once burst is exhausted")
	}
}

func TestAllowRefillsOverTime(t *testing.T) {
	l := New(10, 1, time.Minute)
	if !l.Allow("a") {
		t.Fatal("expected first request to be allowed")
	}
	if l.Allow("a") {
		t.Fatal("expected second immediate request to be denied")
	}
	time.Sleep(150 * time.Millisecond) // ~1.5 tokens at 10/s
	if !l.Allow("a") {
		t.Fatal("expected request to be allowed after refill")
	}
}

func TestAllowKeysAreIndependent(t *testing.T) {
	l := New(1, 1, time.Minute)
	if !l.Allow("a") {
		t.Fatal("expected first request for key a to be allowed")
	}
	if !l.Allow("b") {
		t.Fatal("key b must not be throttled by key a's usage")
	}
}

func TestCleanupEvictsIdleBuckets(t *testing.T) {
	l := New(1, 1, 50*time.Millisecond)
	l.Allow("a")
	if len(l.buckets) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(l.buckets))
	}
	time.Sleep(100 * time.Millisecond)
	l.Cleanup()
	if len(l.buckets) != 0 {
		t.Fatalf("expected idle bucket to be evicted, got %d remaining", len(l.buckets))
	}
}
