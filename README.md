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
