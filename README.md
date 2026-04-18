# MusicGuessr — Backend

Stateless Go HTTP backend for the MusicGuessr application. Resolves Hitster QR codes to track metadata (artist, title, year, artwork) and YouTube video IDs. Also stores and serves custom decks via S3-compatible object storage.

## Repo layout

| Path | Responsibility |
|------|---------------|
| `cmd/server/` | HTTP server, CORS, route registration |
| `internal/resolver/` | Loads Hitster game DB from Azure Blob, resolves QR → Spotify track ID |
| `internal/itunes/` | iTunes Search API — title, artist, year, artwork, Apple Music URL |
| `internal/youtube/` | Invidious API proxy — finds YouTube video ID, result scoring |
| `internal/metadata/` | Parallel metadata provider chain (iTunes, MusicBrainz, Deezer, Discogs, TheAudioDB) |
| `internal/deck/` | Custom deck create/get handlers + per-card YouTube URL validation |
| `internal/deckstore/` | DeckStore interface — `local`, `s3`, `memory` implementations |

## Prerequisites

- Go 1.23+
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

### YouTube / Invidious

| Variable | Default | Description |
|----------|---------|-------------|
| `INVIDIOUS_INSTANCES` | `https://iv.melmac.space,https://invidious.darkness.services` | Comma-separated list of Invidious instances used for YouTube search and video metadata lookups. Instances are tried in order; first successful response wins. |

### Metadata cache

| Variable | Default | Description |
|----------|---------|-------------|
| `METADATA_CACHE_TTL_SECONDS` | `86400` (24 h) | TTL for the in-memory track metadata cache. Reduce to pick up metadata changes sooner; increase to lower external API traffic. |

### Optional metadata providers

| Variable | Default | Description |
|----------|---------|-------------|
| `DISCOGS_TOKEN` | _(unset)_ | Discogs API personal access token. If unset, the Discogs provider is skipped. Get one at [discogs.com/settings/developers](https://www.discogs.com/settings/developers). |
| `THEAUDIODB_KEY` | `1` (public) | TheAudioDB API key. The default public key `1` works but is rate-limited. |

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
