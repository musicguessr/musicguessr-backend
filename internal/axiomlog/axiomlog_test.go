package axiomlog

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNew_NotConfiguredReturnsNil(t *testing.T) {
	t.Setenv("AXIOM_TOKEN", "")
	t.Setenv("AXIOM_DATASET", "")
	if w := New(); w != nil {
		t.Fatalf("got %+v, want nil when env vars are unset", w)
	}
}

func TestNew_OnlyTokenSetReturnsNil(t *testing.T) {
	t.Setenv("AXIOM_TOKEN", "tok")
	t.Setenv("AXIOM_DATASET", "")
	if w := New(); w != nil {
		t.Fatal("expected nil when AXIOM_DATASET is missing")
	}
}

func TestWrite_NeverErrorsAndReportsFullLength(t *testing.T) {
	w := &Writer{client: &http.Client{}, url: "http://127.0.0.1:0/unreachable"}
	line := []byte(`{"msg":"hello"}` + "\n")
	n, err := w.Write(line)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if n != len(line) {
		t.Fatalf("got n=%d, want %d", n, len(line))
	}
}

func TestFlush_SendsBatchedBodyToIngestEndpoint(t *testing.T) {
	var (
		gotAuth        string
		gotContentType string
		gotOrgID       string
		gotBody        []byte
		calls          int32
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		gotOrgID = r.Header.Get("X-Axiom-Org-Id")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	w := &Writer{
		client: srv.Client(),
		url:    srv.URL,
		token:  "sekret",
		orgID:  "org123",
	}
	_, _ = w.Write([]byte(`{"msg":"one"}` + "\n"))
	_, _ = w.Write([]byte(`{"msg":"two"}` + "\n"))
	w.flush()

	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("got %d requests, want 1", calls)
	}
	if gotAuth != "Bearer sekret" {
		t.Errorf("got Authorization %q", gotAuth)
	}
	if gotContentType != "application/x-ndjson" {
		t.Errorf("got Content-Type %q", gotContentType)
	}
	if gotOrgID != "org123" {
		t.Errorf("got X-Axiom-Org-Id %q", gotOrgID)
	}
	want := `{"msg":"one"}` + "\n" + `{"msg":"two"}` + "\n"
	if string(gotBody) != want {
		t.Errorf("got body %q, want %q", gotBody, want)
	}
}

func TestFlush_EmptyBatchDoesNotSendRequest(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	w := &Writer{client: srv.Client(), url: srv.URL, token: "t"}
	w.flush()

	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("got %d requests for an empty batch, want 0", calls)
	}
}

func TestWrite_AutoFlushesAtBatchLimit(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	w := &Writer{client: srv.Client(), url: srv.URL, token: "t", kick: make(chan struct{}, 1), stop: make(chan struct{})}
	go w.flushLoop()
	t.Cleanup(w.Close)
	for i := 0; i < maxBatchEvents; i++ {
		_, _ = w.Write([]byte(`{"i":1}` + "\n"))
	}

	// Well under flushInterval, so a request here came from the batch-full
	// kick rather than the periodic ticker.
	deadline := time.Now().Add(flushInterval / 2)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&calls) == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("got %d requests, want 1 triggered by hitting the batch size limit", atomic.LoadInt32(&calls))
}

func TestWrite_DoesNotBlockOnSlowIngest(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	w := &Writer{client: srv.Client(), url: srv.URL, token: "t", kick: make(chan struct{}, 1), stop: make(chan struct{})}
	go w.flushLoop()

	done := make(chan struct{})
	go func() {
		for i := 0; i < maxBatchEvents*3; i++ {
			_, _ = w.Write([]byte(`{"i":1}` + "\n"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Write blocked while the ingest endpoint was hanging")
	}
}

func TestWrite_DropsLinesPastBufferCap(t *testing.T) {
	w := &Writer{}
	line := bytes.Repeat([]byte("x"), 1<<20)
	for i := 0; i < 10; i++ {
		_, _ = w.Write(line)
	}
	if w.batch.Len() > maxBufferBytes {
		t.Fatalf("buffer grew to %d bytes, cap is %d", w.batch.Len(), maxBufferBytes)
	}
}

func TestClose_FlushesRemainingBatch(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	w := &Writer{client: srv.Client(), url: srv.URL, token: "t", stop: make(chan struct{})}
	go w.flushLoop()
	_, _ = w.Write([]byte(`{"msg":"bye"}` + "\n"))
	w.Close()

	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("got %d requests after Close, want 1", calls)
	}
}

func TestFlushLoop_FlushesPeriodically(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timer-based test in short mode")
	}
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	w := &Writer{client: srv.Client(), url: srv.URL, token: "t", stop: make(chan struct{})}
	go w.flushLoop()
	t.Cleanup(w.Close)

	_, _ = w.Write([]byte(`{"msg":"tick"}` + "\n"))

	deadline := time.Now().Add(flushInterval + 2*time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&calls) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("expected flushLoop's ticker to flush the batch, but no request was made in time")
}
