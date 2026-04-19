package deck

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/musicguessr/musicguessr-backend/internal/deckstore"
)

// mockStore is an in-memory Store implementation for testing.
type mockStore struct {
	data map[string][]byte
	err  error
}

func newMockStore() *mockStore {
	return &mockStore{data: make(map[string][]byte)}
}

func (m *mockStore) Put(_ context.Context, id string, data []byte) error {
	if m.err != nil {
		return m.err
	}
	m.data[id] = data
	return nil
}

func (m *mockStore) Get(_ context.Context, id string) ([]byte, error) {
	if m.err != nil {
		return nil, m.err
	}
	d, ok := m.data[id]
	if !ok {
		return nil, deckstore.ErrNotFound
	}
	return d, nil
}

func TestGetDeck_MethodNotAllowed(t *testing.T) {
	h := NewHandler(newMockStore())
	req := httptest.NewRequest(http.MethodPost, "/api/deck/abc", nil)
	rr := httptest.NewRecorder()
	h.GetDeck(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("got %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestGetDeck_MissingID(t *testing.T) {
	h := NewHandler(newMockStore())
	req := httptest.NewRequest(http.MethodGet, "/api/deck/", nil)
	rr := httptest.NewRecorder()
	h.GetDeck(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("got %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestGetDeck_InvalidIDFormat(t *testing.T) {
	h := NewHandler(newMockStore())

	// IDs must match ^[0-9A-Za-z]{1,32}$ — these are valid URL paths but fail the regex
	for _, id := range []string{"abc-def", "abc.def", strings.Repeat("a", 33)} {
		req := httptest.NewRequest(http.MethodGet, "/api/deck/"+id, nil)
		rr := httptest.NewRecorder()
		h.GetDeck(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Errorf("id=%q: got %d, want %d", id, rr.Code, http.StatusBadRequest)
		}
	}
}

func TestGetDeck_NotFound(t *testing.T) {
	h := NewHandler(newMockStore())
	req := httptest.NewRequest(http.MethodGet, "/api/deck/unknownid123", nil)
	rr := httptest.NewRecorder()
	h.GetDeck(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("got %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestGetDeck_Expired(t *testing.T) {
	store := newMockStore()
	deck := Deck{
		ID:        "expireddeck1",
		CreatedAt: time.Now().Add(-48 * time.Hour),
		ExpiresAt: time.Now().Add(-1 * time.Hour),
		Cards:     []Card{{YtID: "dQw4w9WgXcQ"}},
	}
	data, _ := json.Marshal(deck)
	store.data["expireddeck1"] = data

	h := NewHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/api/deck/expireddeck1", nil)
	rr := httptest.NewRecorder()
	h.GetDeck(rr, req)

	if rr.Code != http.StatusGone {
		t.Errorf("got %d, want %d (Gone)", rr.Code, http.StatusGone)
	}
}

func TestGetDeck_Success(t *testing.T) {
	store := newMockStore()
	deck := Deck{
		ID:        "validdeck123",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
		Cards: []Card{
			{YtID: "dQw4w9WgXcQ", Title: "Never Gonna Give You Up", Artist: "Rick Astley", Year: 1987},
		},
	}
	data, _ := json.Marshal(deck)
	store.data["validdeck123"] = data

	h := NewHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/api/deck/validdeck123", nil)
	rr := httptest.NewRecorder()
	h.GetDeck(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want %d", rr.Code, http.StatusOK)
	}

	var got Deck
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if got.ID != "validdeck123" {
		t.Errorf("ID: got %q, want %q", got.ID, "validdeck123")
	}
	if len(got.Cards) != 1 || got.Cards[0].YtID != "dQw4w9WgXcQ" {
		t.Errorf("unexpected cards: %+v", got.Cards)
	}
}

func TestCreateDeck_MethodNotAllowed(t *testing.T) {
	h := NewHandler(newMockStore())
	req := httptest.NewRequest(http.MethodGet, "/api/deck", nil)
	rr := httptest.NewRecorder()
	h.CreateDeck(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("got %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestCreateDeck_EmptyCards(t *testing.T) {
	h := NewHandler(newMockStore())
	body := bytes.NewBufferString(`{"cards": []}`)
	req := httptest.NewRequest(http.MethodPost, "/api/deck", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.CreateDeck(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("got %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestCreateDeck_TooManyCards(t *testing.T) {
	h := NewHandler(newMockStore())

	cards := make([]map[string]any, maxCards+1)
	for i := range cards {
		cards[i] = map[string]any{"yt_url": "dQw4w9WgXcQ"}
	}
	body, _ := json.Marshal(map[string]any{"cards": cards})
	req := httptest.NewRequest(http.MethodPost, "/api/deck", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.CreateDeck(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("got %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestCreateDeck_InvalidYtURL(t *testing.T) {
	h := NewHandler(newMockStore())
	body := bytes.NewBufferString(`{"cards": [{"yt_url": "not-a-yt-url"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/deck", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.CreateDeck(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("got %d, want %d", rr.Code, http.StatusUnprocessableEntity)
	}
}

func TestCreateDeck_Success(t *testing.T) {
	h := NewHandler(newMockStore())
	body := bytes.NewBufferString(`{"cards": [{"yt_url": "dQw4w9WgXcQ"}], "ttl": "1week"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/deck", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.CreateDeck(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("got %d, want %d; body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	var resp createResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.ID == "" {
		t.Error("expected non-empty deck ID")
	}
	if resp.ExpiresAt.IsZero() {
		t.Error("expected non-zero ExpiresAt")
	}
	if !strings.HasSuffix(resp.ShareURL, "/deck/"+resp.ID) {
		t.Errorf("ShareURL = %q: expected to end with /deck/<id>", resp.ShareURL)
	}
}
