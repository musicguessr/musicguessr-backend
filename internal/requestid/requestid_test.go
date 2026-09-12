package requestid

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNew_ReturnsDistinctIDs(t *testing.T) {
	a, b := New(), New()
	if a == b {
		t.Fatalf("got two identical IDs %q — expected randomness", a)
	}
	if len(a) != 16 {
		t.Fatalf("got ID length %d, want 16 (8 bytes hex-encoded)", len(a))
	}
}

func TestMiddleware_SetsHeaderAndContext(t *testing.T) {
	var seenInHandler string
	handler := Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seenInHandler = FromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	headerID := rr.Header().Get(Header)
	if headerID == "" {
		t.Fatal("expected X-Request-Id response header to be set")
	}
	if seenInHandler != headerID {
		t.Fatalf("context ID %q does not match response header ID %q", seenInHandler, headerID)
	}
}

func TestFromContext_EmptyWhenNotSet(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := FromContext(req.Context()); got != "" {
		t.Fatalf("got %q, want empty string for a context with no request ID", got)
	}
}
