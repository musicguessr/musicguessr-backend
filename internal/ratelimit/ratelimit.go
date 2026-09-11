// Package ratelimit provides a small in-memory, per-key token-bucket
// limiter. It has no external dependencies (stdlib only, matching the rest
// of this backend) and is sized for a single-process deployment — it does
// not coordinate across replicas.
package ratelimit

import (
	"sync"
	"time"
)

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// Limiter is a per-key token bucket: each key (typically a client IP)
// refills at ratePerSec tokens/second up to burst, and Allow consumes one
// token per call.
type Limiter struct {
	mu         sync.Mutex
	buckets    map[string]*bucket
	ratePerSec float64
	burst      float64
	idleTTL    time.Duration
}

// New creates a Limiter allowing ratePerSec sustained requests per key, with
// bursts up to burst. idleTTL controls how long a key's bucket is kept
// around after its last request before Cleanup evicts it.
func New(ratePerSec float64, burst float64, idleTTL time.Duration) *Limiter {
	return &Limiter{
		buckets:    make(map[string]*bucket),
		ratePerSec: ratePerSec,
		burst:      burst,
		idleTTL:    idleTTL,
	}
}

// Allow reports whether a request for key should proceed, consuming one
// token if so.
func (l *Limiter) Allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, lastSeen: now}
		l.buckets[key] = b
	} else {
		elapsed := now.Sub(b.lastSeen).Seconds()
		b.tokens += elapsed * l.ratePerSec
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.lastSeen = now
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Cleanup evicts buckets untouched for longer than idleTTL. Call it
// periodically (e.g. from a background goroutine) so a limiter serving many
// distinct client IPs doesn't grow its map forever.
func (l *Limiter) Cleanup() {
	cutoff := time.Now().Add(-l.idleTTL)
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, b := range l.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
}

// StartCleanup runs Cleanup on the given interval until stop is closed.
func (l *Limiter) StartCleanup(interval time.Duration, stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				l.Cleanup()
			case <-stop:
				return
			}
		}
	}()
}
