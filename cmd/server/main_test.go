package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeHTMLEntities(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"AT&amp;T", "AT&T"},
		{"&quot;quoted&quot;", `"quoted"`},
		{"it&#39;s", "it's"},
		{"it&apos;s", "it's"},
		{"&lt;tag&gt;", "<tag>"},
		{"no entities here", "no entities here"},
		{"multiple &amp; &lt;entities&gt;", "multiple & <entities>"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := decodeHTMLEntities(tc.in)
			if got != tc.want {
				t.Errorf("decodeHTMLEntities(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCORS_Preflight(t *testing.T) {
	handler := cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("OPTIONS: got status %d, want %d", rr.Code, http.StatusNoContent)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("OPTIONS: missing Access-Control-Allow-Origin: *")
	}
}

func TestCORS_PassThrough(t *testing.T) {
	handler := cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("GET: got status %d, want %d", rr.Code, http.StatusOK)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("GET: missing Access-Control-Allow-Origin: *")
	}
}

func TestGzipMiddleware_WithoutEncoding(t *testing.T) {
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello world"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("Content-Encoding") == "gzip" {
		t.Error("expected no gzip encoding when Accept-Encoding is absent")
	}
	if rr.Body.String() != "hello world" {
		t.Errorf("body = %q, want %q", rr.Body.String(), "hello world")
	}
}

func TestGzipMiddleware_WithGzipEncoding(t *testing.T) {
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello world"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("expected Content-Encoding: gzip")
	}

	r, err := gzip.NewReader(rr.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer func() { _ = r.Close() }()

	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("io.ReadAll: %v", err)
	}
	if string(body) != "hello world" {
		t.Errorf("decompressed body = %q, want %q", string(body), "hello world")
	}
}

func TestWriteJSON(t *testing.T) {
	rr := httptest.NewRecorder()
	writeJSON(rr, http.StatusCreated, map[string]string{"key": "value"})

	if rr.Code != http.StatusCreated {
		t.Errorf("status: got %d, want %d", rr.Code, http.StatusCreated)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
	}
	if !strings.Contains(rr.Body.String(), `"key"`) {
		t.Errorf("body = %q: expected JSON key", rr.Body.String())
	}
}

func TestHealthHandler(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	t.Run("GET returns 200 with status ok", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("got %d, want %d", rr.Code, http.StatusOK)
		}
		if !strings.Contains(rr.Body.String(), `"ok"`) {
			t.Errorf("body = %q: expected status ok", rr.Body.String())
		}
	})

	t.Run("POST returns 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/health", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("got %d, want %d", rr.Code, http.StatusMethodNotAllowed)
		}
	})
}

func TestHandleClientError(t *testing.T) {
	t.Run("valid report returns 204", func(t *testing.T) {
		body := `{"message":"boom","context":"scanner-timeout"}`
		req := httptest.NewRequest(http.MethodPost, "/api/client-error", strings.NewReader(body))
		rr := httptest.NewRecorder()
		handleClientError(rr, req)

		if rr.Code != http.StatusNoContent {
			t.Errorf("got %d, want %d", rr.Code, http.StatusNoContent)
		}
	})

	t.Run("empty message still returns 204 without panicking", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/client-error", strings.NewReader(`{}`))
		rr := httptest.NewRecorder()
		handleClientError(rr, req)

		if rr.Code != http.StatusNoContent {
			t.Errorf("got %d, want %d", rr.Code, http.StatusNoContent)
		}
	})

	t.Run("malformed JSON still returns 204, never an error to the reporter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/client-error", strings.NewReader(`not json`))
		rr := httptest.NewRecorder()
		handleClientError(rr, req)

		if rr.Code != http.StatusNoContent {
			t.Errorf("got %d, want %d", rr.Code, http.StatusNoContent)
		}
	})

	t.Run("oversized body is rejected, not read into memory unbounded", func(t *testing.T) {
		huge := bytes.Repeat([]byte("a"), 20<<10)
		body := `{"message":"` + string(huge) + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/client-error", strings.NewReader(body))
		rr := httptest.NewRecorder()
		handleClientError(rr, req)

		// MaxBytesReader makes json.Decode fail once the 8KB cap is hit —
		// handleClientError treats that the same as any other malformed body.
		if rr.Code != http.StatusNoContent {
			t.Errorf("got %d, want %d", rr.Code, http.StatusNoContent)
		}
	})

	t.Run("GET returns 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/client-error", nil)
		rr := httptest.NewRecorder()
		handleClientError(rr, req)

		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("got %d, want %d", rr.Code, http.StatusMethodNotAllowed)
		}
	})
}

func TestTruncateField(t *testing.T) {
	short := "hello"
	if got := truncateField(short); got != short {
		t.Errorf("got %q, want unchanged %q", got, short)
	}

	long := strings.Repeat("x", clientErrorFieldLimit+500)
	got := truncateField(long)
	if len(got) <= clientErrorFieldLimit {
		t.Errorf("expected truncation marker appended, got length %d", len(got))
	}
	if !strings.HasSuffix(got, "…(truncated)") {
		t.Errorf("got %q, want a truncation suffix", got[len(got)-20:])
	}
}
