package deckstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type s3Config struct {
	endpoint  string
	bucket    string
	accessKey string
	secretKey string
	region    string
}

type s3Store struct {
	cfg    s3Config
	client *http.Client
}

func newS3(cfg s3Config) (*s3Store, error) {
	if cfg.endpoint == "" {
		return nil, fmt.Errorf("deckstore/s3: DECK_STORAGE_ENDPOINT is required")
	}
	if cfg.bucket == "" {
		return nil, fmt.Errorf("deckstore/s3: DECK_STORAGE_BUCKET is required")
	}
	if cfg.accessKey == "" || cfg.secretKey == "" {
		return nil, fmt.Errorf("deckstore/s3: DECK_STORAGE_ACCESS_KEY_ID and DECK_STORAGE_SECRET_ACCESS_KEY are required")
	}
	if cfg.region == "" {
		cfg.region = "auto"
	}
	return &s3Store{
		cfg:    cfg,
		client: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        20,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     30 * time.Second,
			},
		},
	}, nil
}

func (s *s3Store) objectURL(id string) string {
	base := strings.TrimRight(s.cfg.endpoint, "/")
	return fmt.Sprintf("%s/%s/%s.json", base, s.cfg.bucket, id)
}

func (s *s3Store) Put(ctx context.Context, id string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.objectURL(id), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	signRequest(req, s.cfg.accessKey, s.cfg.secretKey, s.cfg.region, data)

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("deckstore/s3: PUT returned %d: %s", resp.StatusCode, body)
	}
	return nil
}

func (s *s3Store) Get(ctx context.Context, id string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.objectURL(id), nil)
	if err != nil {
		return nil, err
	}
	signRequest(req, s.cfg.accessKey, s.cfg.secretKey, s.cfg.region, nil)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("deckstore/s3: GET returned %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MB max per deck
}
