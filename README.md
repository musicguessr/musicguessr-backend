# MusicGuessr — Backend

Stateless Go HTTP backend for the MusicGuessr application. Resolves Hitster QR codes to track metadata (artist, title, year, artwork) and YouTube video IDs. Also stores and serves custom decks via S3-compatible object storage.

## Repo layout

| Path | Responsibility |
|------|---------------|
| `cmd/server/` | HTTP server, CORS, route registration |
| `internal/resolver/` | Loads Hitster game DB from Azure Blob, resolves QR → Spotify track ID |
| `internal/itunes/` | iTunes Search API — title, artist, year, artwork, Apple Music URL |
| `internal/youtube/` | Finds a YouTube video ID via yt-dlp, result scoring |
| `internal/metadata/` | Parallel metadata provider chain (iTunes, MusicBrainz, Deezer, Discogs, TheAudioDB) |
| `internal/deck/` | Custom deck create/get handlers + per-card YouTube URL validation |
| `internal/deckstore/` | DeckStore interface — `local`, `s3`, `memory` implementations |
| `internal/rcache/` | Persistent two-tier cache (Valkey hot tier + S3 permanent tier) for YouTube/metadata lookups |

## Prerequisites

- Go 1.27+
- Docker (optional)

## Quick start (local)

```bash
go run ./cmd/server
# → http://localhost:8080
```

## Environment variables

### Server

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | TCP port the HTTP server listens on |
| `LOG_LEVEL` | _(unset)_ | Set to `debug` to enable verbose structured logging |

### YouTube

YouTube search/metadata/playlist lookups shell out to `yt-dlp` (see `internal/youtube/ytdlp.go`) — no configuration required, but the `python3.13` interpreter and the `yt-dlp` package (installed via `PYTHONPATH`, see the Dockerfile) must be present on `PATH`.

### Metadata cache

| Variable | Default | Description |
|----------|---------|-------------|
| `METADATA_CACHE_TTL_SECONDS` | `86400` (24 h) | TTL for the in-memory track metadata cache (used only when the persistent cache below isn't configured — see `RESOLVE_CACHE_TTL_SECONDS`). Reduce to pick up metadata changes sooner; increase to lower external API traffic. |

### Persistent resolve cache (`internal/rcache`)

Caches both the metadata-provider result and the yt-dlp YouTube search result, keyed by (normalized) artist/title, so scanning the same Hitster card twice doesn't repeat either lookup. Two independent, optional tiers — either or both may be configured; with neither set, the app falls back to the in-process, restart-losing caches each package already has on its own.

| Variable | Default | Description |
|----------|---------|-------------|
| `VALKEY_ADDR` | _(unset — tier disabled)_ | `host:port` of a Valkey/Redis server. Fast "hot" tier, entries expire after `RESOLVE_CACHE_TTL_SECONDS`. |
| `VALKEY_PASSWORD` | _(unset)_ | Password for `AUTH`, if the server requires one. |
| `VALKEY_DB` | `0` | Logical DB index (`SELECT`). |
| `RESOLVE_CACHE_TTL_SECONDS` | `2592000` (30 days) | TTL applied to entries written to the Valkey tier. |
| `RESOLVE_CACHE_PROVIDER` | _(unset — tier disabled)_ | `s3` (or `local`/`memory`) — same shape as `DECK_STORAGE_PROVIDER` below, see `deckstore.NewWithPrefix`. Permanent tier, no expiry; safe to point at the same bucket as `DECK_STORAGE_*` (cache keys are hashed and namespaced, so they can't collide with deck IDs). |
| `RESOLVE_CACHE_ENDPOINT` / `_BUCKET` / `_ACCESS_KEY_ID` / `_SECRET_ACCESS_KEY` / `_REGION` | _(required for s3)_ | Same meaning as the `DECK_STORAGE_*` equivalents below. |

### Optional metadata providers

| Variable | Default | Description |
|----------|---------|-------------|
| `DISCOGS_TOKEN` | _(unset)_ | Discogs API personal access token. If unset, the Discogs provider is skipped. Get one at [discogs.com/settings/developers](https://www.discogs.com/settings/developers). |
| `THEAUDIODB_KEY` | `1` (public) | TheAudioDB API key. The default public key `1` works but is rate-limited. |
| `SPOTIFY_CLIENT_ID` / `SPOTIFY_CLIENT_SECRET` | _(unset — disabled)_ | Client Credentials Flow (app-only, no user auth) for a dedicated Spotify app — see [developer.spotify.com](https://developer.spotify.com/dashboard) ("Web API" only under "Which API/SDKs are you planning to use?"; the Redirect URI field is required by the form but unused by this flow). When set, `/api/resolve` fetches the exact track ID's own Spotify catalog data and prefers it over the fanout below — see "Metadata accuracy" below for why this matters. A circuit breaker makes stale/rotated credentials fail safe (falls back to the fanout) rather than breaking requests. |

### Metadata accuracy & known limitations

`/api/resolve` identifies a card's exact Spotify track ID from the QR code, then looks up its title/artist/year/artwork across the providers above (plus Spotify's own catalog, when configured) and combines the answers by majority vote.

For most tracks this works reliably. For **older or less common recordings — TV and film themes especially** — public music databases very often only have a *re-recording* or cover version indexed, not the original: re-recording a theme is usually cheaper to license than clearing the original master. When that's what these providers have, that's what gets shown — sometimes with the wrong year or artist for what's printed on the physical card. We've shipped targeted fixes for this (an earlier-dated, artist-matching iTunes candidate is preferred over the API's raw top match; a tie between providers on the year now favors the older value, since a re-recording is always dated later than the original, never earlier; the direct Spotify catalog lookup above is authoritative for the exact track when available), but this remains fundamentally bounded by what these third-party catalogs have indexed — we can reduce it, not promise it away for every card.

We're sorry when this happens — it can throw off an actual round of the game, and we know that's frustrating. If you spot a card with a wrong year, artist, or track, an issue on this repo with the card's title/deck (or a photo, like the one that led to the fixes above) genuinely helps us investigate.

### Custom decks — deck storage

| Variable | Default | Description |
|----------|---------|-------------|
| `DECK_STORAGE_PROVIDER` | `local` | Storage backend. One of `local` (filesystem), `s3` (S3-compatible), `memory` (in-process, for tests). |
| `DECK_STORAGE_PATH` | `./data/decks` | Directory for deck JSON files. Used only when `DECK_STORAGE_PROVIDER=local`. |
| `DECK_STORAGE_ENDPOINT` | _(required for s3)_ | S3-compatible endpoint URL, e.g. `https://<account>.r2.cloudflarestorage.com` or `https://s3.<region>.io.cloud.ovh.net`. |
| `DECK_STORAGE_BUCKET` | _(required for s3)_ | Bucket name. |
| `DECK_STORAGE_ACCESS_KEY_ID` | _(required for s3)_ | S3 access key ID. |
| `DECK_STORAGE_SECRET_ACCESS_KEY` | _(required for s3)_ | S3 secret access key. |
| `DECK_STORAGE_REGION` | `auto` | S3 region. Use `auto` for Cloudflare R2; set the actual region for OVH/AWS. |

### Custom decks — share URL

| Variable | Default | Description |
|----------|---------|-------------|
| `FRONTEND_URL` | _(unset)_ | Public base URL of the frontend, e.g. `https://musicguessr.example.com`. Used to build the `share_url` returned by `POST /api/deck`. If unset, `share_url` will be a relative `/deck/<id>` path. |

## Example — run locally with S3 storage

```bash
export DECK_STORAGE_PROVIDER=s3
export DECK_STORAGE_ENDPOINT=https://<account_id>.r2.cloudflarestorage.com
export DECK_STORAGE_BUCKET=musicguessr-decks
export DECK_STORAGE_ACCESS_KEY_ID=your_access_key
export DECK_STORAGE_SECRET_ACCESS_KEY=your_secret_key
export DECK_STORAGE_REGION=auto
export FRONTEND_URL=http://localhost:4200
go run ./cmd/server
```

## Example — run locally with filesystem storage (no cloud required)

```bash
# Default — decks saved to ./data/decks/
go run ./cmd/server
```

## Docker

```bash
docker build -t musicguessr-backend:local .
docker run --rm -p 8080:8080 \
  -e DECK_STORAGE_PROVIDER=local \
  musicguessr-backend:local
```

## License

MIT
