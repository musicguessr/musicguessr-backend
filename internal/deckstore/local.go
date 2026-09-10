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
	dest := filepath.Join(s.dir, id+".json")
	// Write to a temp file in the same directory (so the rename is on the same
	// filesystem, and therefore atomic) then rename over the destination.
	// os.WriteFile truncates in place; a process kill/crash mid-write would
	// otherwise leave a permanently corrupted, unrecoverable deck file, since
	// decks are immutable and Put is never retried for the same id.
	tmp, err := os.CreateTemp(s.dir, id+".*.tmp")
	if err != nil {
		return fmt.Errorf("deckstore/local: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("deckstore/local: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("deckstore/local: close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("deckstore/local: chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("deckstore/local: rename temp file: %w", err)
	}
	return nil
}

func (s *localStore) Get(_ context.Context, id string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	return data, err
}
