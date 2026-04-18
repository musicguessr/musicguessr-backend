package deckstore

import (
	"context"
	"sync"
)

type memoryStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func newMemory() *memoryStore {
	return &memoryStore{data: make(map[string][]byte)}
}

func (s *memoryStore) Put(_ context.Context, id string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[id] = data
	return nil
}

func (s *memoryStore) Get(_ context.Context, id string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.data[id]
	if !ok {
		return nil, ErrNotFound
	}
	return d, nil
}
