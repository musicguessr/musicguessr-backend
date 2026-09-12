package deckstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	// id may contain "/" (rcache namespaces its keys this way, e.g.
	// "metadata/<hash>", to mirror the folder-like prefixes used on the S3
	// tier) — the parent directory isn't guaranteed to exist yet.
	destDir := filepath.Dir(dest)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("deckstore/local: mkdir %s: %w", destDir, err)
	}
	// Write to a temp file in the same directory (so the rename is on the same
	// filesystem, and therefore atomic) then rename over the destination.
	// os.WriteFile truncates in place; a process kill/crash mid-write would
	// otherwise leave a permanently corrupted, unrecoverable deck file, since
	// decks are immutable and Put is never retried for the same id.
	tmp, err := os.CreateTemp(destDir, filepath.Base(id)+".*.tmp")
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

func (s *localStore) Delete(_ context.Context, id string) error {
	err := os.Remove(filepath.Join(s.dir, id+".json"))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deckstore/local: remove %s: %w", id, err)
	}
	return nil
}

// List is intentionally non-recursive (deck IDs, the only thing it's
// actually used for — see deck.CleanupExpired — are never nested/namespaced
// like rcache's keys can be). A namespaced id written via Put would not be
// found by List as it stands.
func (s *localStore) List(_ context.Context) ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("deckstore/local: read dir: %w", err)
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// Skip Put's temp files (id.*.tmp) — a crash mid-write can leave one
		// behind, and it's not a complete/valid deck object.
		name := e.Name()
		if ext := filepath.Ext(name); ext == ".json" {
			ids = append(ids, strings.TrimSuffix(name, ext))
		}
	}
	return ids, nil
}
