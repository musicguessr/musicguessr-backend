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

// captureLogs points the default logger at a buffer for the test and returns
// the decoded JSON records written so far.
func captureLogs(t *testing.T) func() []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() []map[string]any {
		var out []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("bad log line %q: %v", line, err)
			}
			out = append(out, m)
		}
		return out
	}
}

func postClientError(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/client-error", strings.NewReader(body))
	rr := httptest.NewRecorder()
	handleClientError(rr, req)
	return rr
}

func TestHandleClientError_LogsDetailsAsNestedFields(t *testing.T) {
	logs := captureLogs(t)
	postClientError(`{"message":"QR not detected","context":"scanner-timeout","details":{"detector":"jsQR","fps":3.5,"video_w":720,"canvas_poisoned":false}}`)

	recs := logs()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want 1", len(recs))
	}
	r := recs[0]
	if r["msg"] != "client error report" || r["level"] != "WARN" {
		t.Errorf("unexpected record: %v", r)
	}
	d, ok := r["details"].(map[string]any)
	if !ok {
		t.Fatalf("details missing or not an object: %v", r["details"])
	}
	if d["detector"] != "jsQR" || d["fps"] != 3.5 || d["video_w"] != float64(720) || d["canvas_poisoned"] != false {
		t.Errorf("details = %v", d)
	}
}

func TestHandleClientError_InfoLevelIsAnEventNotAWarning(t *testing.T) {
	logs := captureLogs(t)
	postClientError(`{"message":"QR decoded","context":"scanner-success","level":"info","details":{"time_to_scan_ms":2400}}`)

	r := logs()[0]
	if r["msg"] != "client event" || r["level"] != "INFO" || r["context"] != "scanner-success" {
		t.Errorf("unexpected record: %v", r)
	}
}

func TestHandleClientError_UnknownLevelStaysAWarning(t *testing.T) {
	logs := captureLogs(t)
	postClientError(`{"message":"x","level":"debug"}`)
	if r := logs()[0]; r["level"] != "WARN" || r["msg"] != "client error report" {
		t.Errorf("a client must not be able to pick an arbitrary level: %v", r)
	}
}

func TestSanitizeDetails(t *testing.T) {
	long := strings.Repeat("a", 500)
	in := map[string]any{
		"ok_key":                "value",
		"num":                   float64(12),
		"flag":                  true,
		"Bad-Key":               "uppercase and dash are rejected",
		"nested":                map[string]any{"a": 1},
		"list":                  []any{1, 2},
		"nothing":               nil,
		"long_value":            long,
		strings.Repeat("k", 41): "key too long",
		"":                      "empty key",
	}
	got := map[string]any{}
	for _, a := range sanitizeDetails(in) {
		attr := a.(slog.Attr)
		got[attr.Key] = attr.Value.Any()
	}

	for _, k := range []string{"ok_key", "num", "flag", "long_value"} {
		if _, ok := got[k]; !ok {
			t.Errorf("%q should be kept, got %v", k, got)
		}
	}
	for _, k := range []string{"Bad-Key", "nested", "list", "nothing", "", strings.Repeat("k", 41)} {
		if _, ok := got[k]; ok {
			t.Errorf("%q should be dropped", k)
		}
	}
	if s, _ := got["long_value"].(string); len(s) > maxDetailValueLen+4 {
		t.Errorf("long value not truncated: %d bytes", len(s))
	}
}

func TestSanitizeDetails_CapsKeyCount(t *testing.T) {
	in := map[string]any{}
	for i := 0; i < 100; i++ {
		in["k"+strings.Repeat("x", i%5)+string(rune('a'+i%26))+string(rune('a'+i/26))] = "v"
	}
	if n := len(sanitizeDetails(in)); n > maxDetailKeys {
		t.Errorf("kept %d keys, cap is %d", n, maxDetailKeys)
	}
}
