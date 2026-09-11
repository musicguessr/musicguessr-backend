package deckstore

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
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
		return nil, fmt.Errorf("deckstore/s3: *_ENDPOINT is required")
	}
	if cfg.bucket == "" {
		return nil, fmt.Errorf("deckstore/s3: *_BUCKET is required")
	}
	if cfg.accessKey == "" || cfg.secretKey == "" {
		return nil, fmt.Errorf("deckstore/s3: *_ACCESS_KEY_ID and *_SECRET_ACCESS_KEY are required")
	}
	if cfg.region == "" {
		// "auto" is meaningful for Cloudflare R2 but not for real AWS S3 or
		// other S3-compatible providers — log it so a deployer pointing at a
		// provider that needs a real region sees why every request suddenly
		// fails signature verification, instead of silently getting "auto".
		slog.Warn("*_REGION not set, defaulting to \"auto\" (correct for Cloudflare R2; set explicitly for AWS S3 or other providers)")
		cfg.region = "auto"
	}
	return &s3Store{
		cfg: cfg,
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

func (s *s3Store) bucketURL(rawQuery string) string {
	base := strings.TrimRight(s.cfg.endpoint, "/")
	u := fmt.Sprintf("%s/%s", base, s.cfg.bucket)
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	return u
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
	const maxSize = 4 << 20 // 4 MB max per deck
	// Read one byte past the limit so a truncated body can be told apart from
	// one that legitimately ends exactly at maxSize — io.ReadAll(io.LimitReader(...))
	// alone returns err == nil either way, so local/memory (no size limit) and
	// s3 (silently truncating) would behave differently for an oversized deck,
	// surfacing only as a downstream "corrupted deck data" JSON error.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("deckstore/s3: read body: %w", err)
	}
	if len(data) > maxSize {
		return nil, fmt.Errorf("deckstore/s3: object exceeds %d byte limit", maxSize)
	}
	return data, nil
}

func (s *s3Store) Delete(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.objectURL(id), nil)
	if err != nil {
		return err
	}
	signRequest(req, s.cfg.accessKey, s.cfg.secretKey, s.cfg.region, nil)

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	// 204 is S3's normal response; 404 means it's already gone — both count
	// as success per Delete's "not an error to delete a missing id" contract.
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("deckstore/s3: DELETE returned %d: %s", resp.StatusCode, body)
	}
	return nil
}

type s3ListResult struct {
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	Contents              []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
}

// List enumerates every object in the bucket via ListObjectsV2, paginating
// through continuation tokens until IsTruncated is false.
func (s *s3Store) List(ctx context.Context) ([]string, error) {
	var ids []string
	continuationToken := ""
	for {
		q := url.Values{}
		q.Set("list-type", "2")
		q.Set("max-keys", "1000")
		if continuationToken != "" {
			q.Set("continuation-token", continuationToken)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.bucketURL(q.Encode()), nil)
		if err != nil {
			return nil, err
		}
		signRequest(req, s.cfg.accessKey, s.cfg.secretKey, s.cfg.region, nil)

		resp, err := s.client.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("deckstore/s3: read list response: %w", readErr)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("deckstore/s3: LIST returned %d: %s", resp.StatusCode, body)
		}

		var result s3ListResult
		if err := xml.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("deckstore/s3: parse list response: %w", err)
		}
		for _, c := range result.Contents {
			ids = append(ids, strings.TrimSuffix(c.Key, ".json"))
		}

		if !result.IsTruncated || result.NextContinuationToken == "" {
			break
		}
		continuationToken = result.NextContinuationToken
	}
	return ids, nil
}
