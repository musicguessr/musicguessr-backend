package youtube

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound is returned when yt-dlp confirms a video/playlist doesn't
// exist, is private, or was removed — a definitive negative, distinct from
// a transient/infrastructure failure (network error, yt-dlp crash, bad
// output) which is returned as a plain error instead.
var ErrNotFound = errors.New("youtube: resource not found")

const (
	searchTimeout   = 20 * time.Second
	videoTimeout    = 15 * time.Second
	playlistTimeout = 45 * time.Second
)

// ytPythonBin is the interpreter used to run the yt-dlp module. The
// production image has no console-script wrapper on PATH — yt-dlp is
// installed with `pip install --target=` and located via PYTHONPATH — so it
// must be invoked as `python3.13 -m yt_dlp`.
const ytPythonBin = "python3.13"

// notFoundMarkers are substrings yt-dlp prints to stderr for a confirmed
// negative (video/playlist doesn't exist, is private, or was taken down),
// as opposed to a transient network/infra failure.
var notFoundMarkers = []string{
	"video unavailable",
	"this video is unavailable",
	"content isn't available",
	"content is not available",
	"private video",
	"video is no longer available",
	"has been removed",
	"does not exist",
	"this playlist does not exist",
	"unable to find playlist",
}

type ytSearchItem struct {
	Type    string
	VideoID string
	Title   string
	Author  string
}

type ytDlpEntry struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Channel  string `json:"channel"`
	Uploader string `json:"uploader"`
}

// ytDlpSlots caps concurrent yt-dlp processes process-wide. Each one is a
// full Python interpreter (~50-100 MB); per-IP rate limits don't bound the
// total, so a burst of cold lookups from many clients (plus cachewarm) could
// otherwise run the Pi out of memory.
var ytDlpSlots = make(chan struct{}, 3)

func runYtDlp(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case ytDlpSlots <- struct{}{}:
		defer func() { <-ytDlpSlots }()
	case <-ctx.Done():
		return nil, fmt.Errorf("yt-dlp: no free slot: %w", ctx.Err())
	}

	fullArgs := append([]string{"-m", "yt_dlp", "--no-warnings", "--ignore-config", "--skip-download", "--dump-json"}, args...)
	cmd := exec.CommandContext(ctx, ytPythonBin, fullArgs...)
	// exec.CommandContext's default Cancel (Process.Kill) only guarantees the
	// process is signaled when ctx is done — if a grandchild process yt-dlp
	// spawns keeps the stdout/stderr pipes open after the kill, cmd.Wait (and
	// therefore cmd.Run, and therefore this whole call) can still block past
	// the context deadline. WaitDelay bounds that: once Cancel has fired and
	// WaitDelay elapses, Go forcibly closes the I/O pipes so Wait returns.
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		stderrStr := stderr.String()
		if isNotFoundError(stderrStr) {
			return nil, ErrNotFound
		}
		slog.Warn("yt-dlp invocation failed", "args", args, "err", err, "stderr", strings.TrimSpace(stderrStr))
		return nil, fmt.Errorf("yt-dlp failed: %w", err)
	}
	return stdout.Bytes(), nil
}

func isNotFoundError(stderr string) bool {
	lower := strings.ToLower(stderr)
	for _, m := range notFoundMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// parseYtDlpEntries decodes yt-dlp's --dump-json output, which prints one
// JSON object per line (one per result/video), not a JSON array.
func parseYtDlpEntries(out []byte) []ytSearchItem {
	var items []ytSearchItem
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var e ytDlpEntry
		if err := json.Unmarshal(line, &e); err != nil {
			slog.Warn("yt-dlp output decode failed", "err", err)
			continue
		}
		if e.ID == "" {
			continue
		}
		author := e.Channel
		if author == "" {
			author = e.Uploader
		}
		items = append(items, ytSearchItem{Type: "video", VideoID: e.ID, Title: e.Title, Author: author})
	}
	return items
}

// ytSearch runs a YouTube search via yt-dlp and returns up to limit results.
// --flat-playlist skips per-video metadata extraction (format lists, etc.),
// which is unnecessary for search — only id/title/channel are needed.
func ytSearch(ctx context.Context, query string, limit int) ([]ytSearchItem, error) {
	out, err := runYtDlp(ctx, searchTimeout, "--flat-playlist", fmt.Sprintf("ytsearch%d:%s", limit, query))
	if err != nil {
		return nil, err
	}
	return parseYtDlpEntries(out), nil
}

// FetchVideoMeta fetches title/author for a single known video ID.
func FetchVideoMeta(ctx context.Context, videoID string) (title, author string, err error) {
	out, err := runYtDlp(ctx, videoTimeout, "--", "https://www.youtube.com/watch?v="+videoID)
	if err != nil {
		return "", "", err
	}
	items := parseYtDlpEntries(out)
	if len(items) == 0 {
		return "", "", ErrNotFound
	}
	return items[0].Title, items[0].Author, nil
}

// PlaylistVideo is a single video entry returned by FetchPlaylist.
type PlaylistVideo struct {
	VideoID string
	Title   string
	Author  string
}

// FetchPlaylist fetches up to maxItems videos from a YouTube playlist by ID.
// --flat-playlist keeps this fast even for large playlists since it skips
// per-video metadata extraction; --playlist-end lets yt-dlp stop paging
// once enough entries are collected instead of fetching the whole playlist.
func FetchPlaylist(ctx context.Context, playlistID string, maxItems int) ([]PlaylistVideo, error) {
	playlistURL := "https://www.youtube.com/playlist?list=" + url.QueryEscape(playlistID)
	out, err := runYtDlp(ctx, playlistTimeout,
		"--flat-playlist", "--playlist-end", strconv.Itoa(maxItems), "--", playlistURL)
	if err != nil {
		return nil, err
	}
	items := parseYtDlpEntries(out)
	videos := make([]PlaylistVideo, 0, len(items))
	for _, it := range items {
		videos = append(videos, PlaylistVideo{VideoID: it.VideoID, Title: it.Title, Author: it.Author})
	}
	return videos, nil
}
