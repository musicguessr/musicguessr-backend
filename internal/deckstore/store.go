package deckstore

import (
	"context"
	"fmt"
	"os"
	"strings"
)

type Store interface {
	Put(ctx context.Context, id string, data []byte) error
	Get(ctx context.Context, id string) ([]byte, error)
}

func New() (Store, error) {
	store, err := NewWithPrefix("DECK_STORAGE", "local")
	if err != nil {
		return nil, err
	}
	return store, nil
}

// NewWithPrefix builds a Store from environment variables named
// "{prefix}_PROVIDER", "{prefix}_PATH", "{prefix}_ENDPOINT", "{prefix}_BUCKET",
// "{prefix}_ACCESS_KEY_ID", "{prefix}_SECRET_ACCESS_KEY" and "{prefix}_REGION"
// — the same shape as the original DECK_STORAGE_* variables, parameterized so
// unrelated features (e.g. a persistent resolve cache) can configure their
// own independent S3/local/memory backend without colliding with deck
// storage's env vars or bucket.
//
// If "{prefix}_PROVIDER" is unset and defaultProvider is "", NewWithPrefix
// returns (nil, nil) — an explicit "not configured" rather than an error, so
// optional features can treat a nil Store as "disabled" instead of failing
// startup.
func NewWithPrefix(prefix, defaultProvider string) (Store, error) {
	provider := strings.ToLower(os.Getenv(prefix + "_PROVIDER"))
	if provider == "" {
		if defaultProvider == "" {
			return nil, nil
		}
		provider = defaultProvider
	}
	switch provider {
	case "local":
		path := os.Getenv(prefix + "_PATH")
		if path == "" {
			path = "./data/decks"
		}
		return newLocal(path)
	case "s3":
		return newS3(s3Config{
			endpoint:  os.Getenv(prefix + "_ENDPOINT"),
			bucket:    os.Getenv(prefix + "_BUCKET"),
			accessKey: os.Getenv(prefix + "_ACCESS_KEY_ID"),
			secretKey: os.Getenv(prefix + "_SECRET_ACCESS_KEY"),
			region:    os.Getenv(prefix + "_REGION"),
		})
	case "memory":
		return newMemory(), nil
	default:
		return nil, fmt.Errorf("deckstore: unknown provider %q", provider)
	}
}
