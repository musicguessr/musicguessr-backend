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
	provider := strings.ToLower(os.Getenv("DECK_STORAGE_PROVIDER"))
	if provider == "" {
		provider = "local"
	}
	switch provider {
	case "local":
		path := os.Getenv("DECK_STORAGE_PATH")
		if path == "" {
			path = "./data/decks"
		}
		return newLocal(path)
	case "s3":
		return newS3(s3Config{
			endpoint:  os.Getenv("DECK_STORAGE_ENDPOINT"),
			bucket:    os.Getenv("DECK_STORAGE_BUCKET"),
			accessKey: os.Getenv("DECK_STORAGE_ACCESS_KEY_ID"),
			secretKey: os.Getenv("DECK_STORAGE_SECRET_ACCESS_KEY"),
			region:    os.Getenv("DECK_STORAGE_REGION"),
		})
	case "memory":
		return newMemory(), nil
	default:
		return nil, fmt.Errorf("deckstore: unknown provider %q", provider)
	}
}
