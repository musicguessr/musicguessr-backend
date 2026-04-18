package deck

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/musicguessr/musicguessr-backend/internal/deckstore"
	"github.com/musicguessr/musicguessr-backend/internal/itunes"
)

var deckIDRe = regexp.MustCompile(`^[0-9A-Za-z]{1,32}$`)

var ttlMap = map[string]time.Duration{
	"1week":   7 * 24 * time.Hour,
	"1month":  30 * 24 * time.Hour,
	"3months": 90 * 24 * time.Hour,
	"6months": 180 * 24 * time.Hour,
	"1year":   365 * 24 * time.Hour,
}

const (
	maxCards    = 300
	defaultTTL  = "3months"
)

type Handler struct {
	store       deckstore.Store
	frontendURL string
}

func NewHandler(store deckstore.Store) *Handler {
	url := strings.TrimRight(os.Getenv("FRONTEND_URL"), "/")
	return &Handler{store: store, frontendURL: url}
}

func (h *Handler) CreateDeck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 2<<20) // 2 MB limit
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid request body"))
		return
	}

	if len(req.Cards) == 0 {
		writeJSON(w, http.StatusBadRequest, errResp("cards array is required and must not be empty"))
		return
	}
	if len(req.Cards) > maxCards {
		writeJSON(w, http.StatusBadRequest, errResp("maximum 300 cards per deck"))
		return
	}

	ttl, ok := ttlMap[req.TTL]
	if !ok {
		ttl = ttlMap[defaultTTL]
	}

	// First pass: validate all URLs synchronously so we fail fast before any network I/O.
	cards := make([]Card, len(req.Cards))
	for i, c := range req.Cards {
		ytID, err := extractYtID(c.YtURL)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errResp("card "+itoa(i)+": "+err.Error()))
			return
		}
		cards[i] = Card{YtID: ytID, Title: c.Title, Artist: c.Artist, Year: c.Year}
	}

	// Second pass: best-effort iTunes enrichment, concurrent with a semaphore (max 10 at once).
	const enrichConcurrency = 10
	sem := make(chan struct{}, enrichConcurrency)
	var wg sync.WaitGroup
	for i, card := range cards {
		if card.Title == "" || card.Artist == "" {
			continue
		}
		wg.Add(1)
		go func(i int, card Card) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cards[i] = enrichCard(r.Context(), card)
		}(i, card)
	}
	wg.Wait()

	id, err := newID()
	if err != nil {
		slog.Error("nanoid generation failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, errResp("failed to generate deck id"))
		return
	}

	now := time.Now().UTC()
	deck := Deck{
		ID:        id,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
		Cards:     cards,
	}

	data, err := json.Marshal(deck)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("failed to encode deck"))
		return
	}

	if err := h.store.Put(r.Context(), id, data); err != nil {
		slog.Error("deck store put failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, errResp("failed to save deck"))
		return
	}

	shareURL := h.frontendURL + "/deck/" + id
	writeJSON(w, http.StatusCreated, createResponse{
		ID:        id,
		ShareURL:  shareURL,
		ExpiresAt: deck.ExpiresAt,
	})
}

func (h *Handler) GetDeck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Path: /api/deck/{id}
	id := strings.TrimPrefix(r.URL.Path, "/api/deck/")
	id = strings.Trim(id, "/")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, errResp("missing deck id"))
		return
	}
	if !deckIDRe.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, errResp("invalid deck id"))
		return
	}

	data, err := h.store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, deckstore.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errResp("deck not found"))
			return
		}
		slog.Error("deck store get failed", "id", id, "err", err)
		writeJSON(w, http.StatusInternalServerError, errResp("failed to retrieve deck"))
		return
	}

	var deck Deck
	if err := json.Unmarshal(data, &deck); err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("corrupted deck data"))
		return
	}

	if time.Now().After(deck.ExpiresAt) {
		writeJSON(w, http.StatusGone, errResp("deck has expired"))
		return
	}

	writeJSON(w, http.StatusOK, deck)
}

func enrichCard(ctx context.Context, card Card) Card {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	track, err := itunes.Search(ctx, card.Artist, card.Title)
	if err != nil {
		return card
	}
	if card.Artwork == "" {
		card.Artwork = track.ArtworkURL
	}
	if card.Year == 0 {
		card.Year = track.Year
	}
	return card
}

func errResp(msg string) map[string]string {
	return map[string]string{"error": msg}
}

func itoa(i int) string {
	return strconv.Itoa(i)
}
