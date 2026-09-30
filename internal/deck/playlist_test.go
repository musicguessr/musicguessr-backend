package deck

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestIsMixPlaylistID(t *testing.T) {
	tests := map[string]bool{
		// The two mixes that failed in production with "playlist type is unviewable".
		"RDi6_G1Axic7Q":                      true,
		"RDtiTpHDIKxrg":                      true,
		"RDCLAK5uy_kmPRjHDECIcuVwnKsx":       true,
		"PLrAXtmErZgOeiKm4sgNOknGvNjby9efdf": false,
		"UUabcdefghijklmnopqrstuv":           false,
		"LLabcdefghijkl":                     false,
	}
	for id, want := range tests {
		if got := isMixPlaylistID(id); got != want {
			t.Errorf("isMixPlaylistID(%q) = %v, want %v", id, got, want)
		}
	}
}

// A Mix must be refused up front with a specific message — without reaching
// yt-dlp, which can never list it.
func TestImportPlaylistHandler_RejectsMixWithoutCallingYtDlp(t *testing.T) {
	for _, raw := range []string{
		"https://www.youtube.com/playlist?list=RDi6_G1Axic7Q",
		"https://www.youtube.com/watch?v=i6_G1Axic7Q&list=RDi6_G1Axic7Q&start_radio=1",
		"RDtiTpHDIKxrg",
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/deck/import-playlist?url="+url.QueryEscape(raw), nil)
		rec := httptest.NewRecorder()
		ImportPlaylistHandler(rec, req)

		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422", raw, rec.Code)
		}
		var body struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error != "youtube mix playlists can't be imported, use a regular playlist" {
			t.Errorf("%s: body = %q (err %v)", raw, rec.Body.String(), err)
		}
	}
}
