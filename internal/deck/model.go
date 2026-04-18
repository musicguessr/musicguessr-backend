package deck

import "time"

type Card struct {
	YtID     string `json:"yt_id"`
	Title    string `json:"title,omitempty"`
	Artist   string `json:"artist,omitempty"`
	Year     int    `json:"year,omitempty"`
	Artwork  string `json:"artwork,omitempty"`
}

type Deck struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Cards     []Card    `json:"cards"`
}

type createRequest struct {
	Cards []cardInput `json:"cards"`
	TTL   string      `json:"ttl"`
}

type cardInput struct {
	YtURL  string `json:"yt_url"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Year   int    `json:"year"`
}

type createResponse struct {
	ID        string    `json:"id"`
	ShareURL  string    `json:"share_url"`
	ExpiresAt time.Time `json:"expires_at"`
}
