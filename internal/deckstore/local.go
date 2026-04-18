package deckstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type localStore struct {
	dir string
}

func newLocal(dir string) (*localStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("deckstore/local: mkdir %s: %w", dir, err)
	}
	return &localStore{dir: dir}, nil
}

func (s *localStore) Put(_ context.Context, id string, data []byte) error {
	return os.WriteFile(filepath.Join(s.dir, id+".json"), data, 0o644)
}

func (s *localStore) Get(_ context.Context, id string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	return data, err
}
