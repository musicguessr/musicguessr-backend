# Red Hat Hardened Images (Project Hummingbird) — hardened, minimal, no
# subscription required. Registry: registry.access.redhat.com (anonymous
# pulls), repo namespace "hi". See musicguessr-backend/README.md.
FROM registry.access.redhat.com/hi/go:1.27 AS build
WORKDIR /src
COPY go.mod .
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -trimpath -o /backend ./cmd/server

# The final "hi/static" image below has no shell, so `RUN mkdir` can't run
# there — pre-create the deck data dir here (this stage still has a shell)
# with the final image's nonroot UID/GID, then COPY it across. Docker seeds a
# fresh named volume from the image directory's contents/permissions on
# first use, so this also fixes ownership for `docker-compose.yml`'s
# backend-decks volume.
RUN mkdir -p /data/decks && chown 65532:65532 /data/decks

# hi/static: CA certificates + tzdata + nonroot user only — no shell, no
# package manager, no C library. Built for exactly this: a statically linked
# (CGO_ENABLED=0) Go binary and nothing else.
FROM registry.access.redhat.com/hi/static:latest
COPY --from=build --chown=65532:65532 --chmod=0755 /backend /backend
COPY --from=build --chown=65532:65532 /data/decks /data/decks
EXPOSE 8080
ENV PORT=8080
ENV DECK_STORAGE_PATH=/data/decks
USER 65532
ENTRYPOINT ["/backend"]
