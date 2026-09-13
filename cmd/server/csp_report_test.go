package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleCSPReport_LogsViolationWithoutQuery(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	body := `{"csp-report":{"document-uri":"https://musicguessr.app/callback?code=secret","blocked-uri":"https://evil.example/x.js?t=1","effective-directive":"script-src-elem","violated-directive":"script-src","disposition":"enforce"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/csp-report", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/csp-report")
	rec := httptest.NewRecorder()
	handleCSPReport(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("expected one JSON log line, got %q: %v", buf.String(), err)
	}
	if entry["msg"] != "csp violation" || entry["directive"] != "script-src-elem" {
		t.Errorf("unexpected log entry: %v", entry)
	}
	if entry["document_uri"] != "https://musicguessr.app/callback" || entry["blocked_uri"] != "https://evil.example/x.js" {
		t.Errorf("query strings must be stripped before logging: %v", entry)
	}
}

func TestHandleCSPReport_IgnoresGarbage(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	for _, tc := range []struct{ method, body string }{
		{http.MethodPost, "not json"},
		{http.MethodPost, `{"csp-report":{}}`},
	} {
		rec := httptest.NewRecorder()
		handleCSPReport(rec, httptest.NewRequest(tc.method, "/api/csp-report", strings.NewReader(tc.body)))
		if rec.Code != http.StatusNoContent {
			t.Errorf("body %q: status = %d, want 204", tc.body, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	handleCSPReport(rec, httptest.NewRequest(http.MethodGet, "/api/csp-report", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status = %d, want 405", rec.Code)
	}
	if buf.Len() != 0 {
		t.Errorf("nothing should be logged for invalid reports, got %q", buf.String())
	}
}
