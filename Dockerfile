# Red Hat Hardened Images (Project Hummingbird) — hardened, minimal, no
# subscription required. Registry: registry.access.redhat.com (anonymous
# pulls), repo namespace "hi". See musicguessr-backend/README.md.
FROM registry.access.redhat.com/hi/go:1.27 AS build
WORKDIR /src
COPY go.mod .
RUN go mod download
COPY . .
# Passed via --build-arg from docker-multiarch.yml (github.sha and the
# current UTC date) — surfaced at runtime on /health. Left at their Go
# zero-value defaults ("unknown", see main.go) for a plain local build.
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.gitCommit=${GIT_COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -trimpath -o /backend ./cmd/server

# The final image below has no shell, so `RUN mkdir` can't run there —
# pre-create the deck data dir here (this stage still has a shell) with the
# final image's nonroot UID/GID, then COPY it across. Docker/Podman seeds a
# fresh named volume from the image directory's contents/permissions on
# first use, so this also fixes ownership for the backend-decks volume.
RUN mkdir -p /data/decks && chown 65532:65532 /data/decks

# yt-dlp is used (as a subprocess from the Go binary) to search/resolve
# YouTube videos and playlists — the public Invidious/Piped API instances
# this previously relied on are no longer reliably available. The "-builder"
# variant keeps a shell/pip for install-time use only; the final stage below
# has neither.
FROM registry.access.redhat.com/hi/python:3.14-builder AS ytdlp-build
COPY requirements-ytdlp.txt /tmp/requirements-ytdlp.txt
RUN pip install --no-cache-dir --require-hashes --target=/tmp/ytdlp-deps -r /tmp/requirements-ytdlp.txt

# hi/python: same nonroot/no-package-manager hardening as hi/static, plus a
# Python 3 interpreter — needed here only to run yt-dlp as a module via
# PYTHONPATH (no console-script wrapper is installed with --target).
FROM registry.access.redhat.com/hi/python:3.14
COPY --from=build --chown=65532:65532 --chmod=0755 /backend /backend
COPY --from=build --chown=65532:65532 /data/decks /data/decks
COPY --from=ytdlp-build --chown=65532:65532 /tmp/ytdlp-deps /opt/ytdlp-deps
EXPOSE 8080
ENV PORT=8080
ENV DECK_STORAGE_PATH=/data/decks
ENV PYTHONPATH=/opt/ytdlp-deps
USER 65532
ENTRYPOINT ["/backend"]
