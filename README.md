# MusicGuessr — Backend

Backend service for the MusicGuessr application. This repository contains a small Go HTTP server and internal packages that handle iTunes and YouTube integration and request resolving.

## Features

- Lightweight HTTP server under `cmd/server`
- Internal packages: `internal/itunes`, `internal/youtube`, `internal/resolver`
- Dockerfile for containerized runs

## Repo layout

- `cmd/server` — application entrypoint
- `internal/itunes` — iTunes-related helpers
- `internal/youtube` — YouTube-related helpers and filters
- `internal/resolver` — request/resolution logic
- `Dockerfile`, `go.mod` — build and dependency files

## Prerequisites

- Go 1.26+
- Docker (optional, for container builds)

## Quick start (local)

1. Clone the repository and enter the folder:

```bash
git clone <your-repo-url> musicguessr-backend
cd musicguessr-backend
```

2. Build and run the server:

```bash
go build ./cmd/server
./server
```

3. Or run with `go run`:

```bash
go run ./cmd/server
```

## Environment variables

The server and metadata providers use several environment variables. Defaults shown where applicable.

- `PORT` — TCP port the HTTP server listens on. Default: `8080`.
- `LOG_LEVEL` — set to `debug` to enable debug logging (optional).
- `INVIDIOUS_INSTANCES` — comma-separated list of Invidious instances used for YouTube lookups. Default includes `https://iv.melmac.space,https://invidious.darkness.services`.
- `METADATA_CACHE_TTL_SECONDS` — TTL for the in-memory metadata cache in seconds. Default: `86400` (24h).
- `DISCOGS_TOKEN` — (optional) Discogs API token. If set, the Discogs provider will be used.
- `THEAUDIODB_KEY` — (optional) TheAudioDB API key. If unset, the public key `1` is used.

Notes:
- The metadata layer performs parallel lookups across multiple providers (iTunes, MusicBrainz, Deezer, Discogs, TheAudioDB) and aggregates results. Provide `DISCOGS_TOKEN`/`THEAUDIODB_KEY` if you want to enable those providers.
- `METADATA_CACHE_TTL_SECONDS` can be tuned to reduce external requests and respect provider rate limits.
- MusicBrainz requires a sensible `User-Agent` header and has rate limits; the code uses a default UA string. Consider adding caching and backoff when running at scale.

Example (macOS / Linux):

```bash
PORT=8080 LOG_LEVEL=debug DISCOGS_TOKEN=your_token go run ./cmd/server
```

## Docker

Build image:

```bash
docker build -t musicguessr-backend:local .
```

Run container:

```bash
docker run --rm -p 8080:8080 musicguessr-backend:local
```

Adjust the port according to the server configuration.

## Contributing

Open issues or PRs. If you're changing module/import paths, make sure to run `go mod tidy` and update any documentation.

## License

Specify a license for the project (e.g. MIT) or add one later.
