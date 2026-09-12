// Package axiomlog ships structured logs to Axiom (axiom.co) — a free-tier
// external log aggregator (see AXIOM_TOKEN/AXIOM_DATASET below) — so
// production issues are actually searchable/gettable without SSHing into
// the Pi and grepping journalctl. Entirely optional: with neither env var
// set, New returns nil and the caller falls back to local-only logging,
// same as every other optional integration in this backend.
package axiomlog

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	maxBatchEvents = 100
	flushInterval  = 3 * time.Second
	requestTimeout = 10 * time.Second
)

// Writer batches raw NDJSON log lines (exactly what slog.NewJSONHandler
// produces, one JSON object per line — Axiom's ingest API accepts that
// format directly under Content-Type: application/x-ndjson) and ships them
// asynchronously. It implements io.Writer so it composes with os.Stderr via
// io.MultiWriter: journalctl still sees every line locally in real time,
// Axiom gets an async, batched copy — a failure shipping to Axiom never
// blocks or drops the local log.
type Writer struct {
	url    string
	token  string
	orgID  string
	client *http.Client

	mu    sync.Mutex
	batch bytes.Buffer
	count int

	stop chan struct{}
}

// New returns nil if AXIOM_TOKEN or AXIOM_DATASET aren't both set.
func New() *Writer {
	token := os.Getenv("AXIOM_TOKEN")
	dataset := os.Getenv("AXIOM_DATASET")
	if token == "" || dataset == "" {
		return nil
	}
	w := &Writer{
		url:    "https://api.axiom.co/v1/datasets/" + dataset + "/ingest",
		token:  token,
		orgID:  os.Getenv("AXIOM_ORG_ID"),
		client: &http.Client{Timeout: requestTimeout},
		stop:   make(chan struct{}),
	}
	go w.flushLoop()
	return w
}

// Write implements io.Writer. Never returns an error for a shipping
// failure — logging must not itself be a reason a request fails — and
// always reports the full byte count written, since the caller (slog, via
// io.MultiWriter) would otherwise treat a short write as an error too.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.batch.Write(p)
	w.count++
	full := w.count >= maxBatchEvents
	w.mu.Unlock()
	if full {
		w.flush()
	}
	return len(p), nil
}

// Close stops the background flush loop and sends any buffered logs — call
// during graceful shutdown so the last few lines of a shutdown sequence
// aren't silently lost.
func (w *Writer) Close() {
	close(w.stop)
	w.flush()
}

func (w *Writer) flushLoop() {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			w.flush()
		case <-w.stop:
			return
		}
	}
}

func (w *Writer) flush() {
	w.mu.Lock()
	if w.batch.Len() == 0 {
		w.mu.Unlock()
		return
	}
	data := make([]byte, w.batch.Len())
	copy(data, w.batch.Bytes())
	w.batch.Reset()
	w.count = 0
	w.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(data))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+w.token)
	req.Header.Set("Content-Type", "application/x-ndjson")
	if w.orgID != "" {
		req.Header.Set("X-Axiom-Org-Id", w.orgID)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		// Deliberately not logged via slog here — a failure shipping logs to
		// Axiom would otherwise recursively write another line into this
		// same batching writer.
		return
	}
	_ = resp.Body.Close()
}
