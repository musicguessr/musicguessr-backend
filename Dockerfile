FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod .
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -trimpath -o /backend ./cmd/server

FROM alpine:3.23
RUN apk update && apk upgrade --no-cache \
    && apk add --no-cache ca-certificates tzdata \
    && rm -rf /var/cache/apk/* \
    && addgroup -S app && adduser -S -G app app \
    && mkdir -p /data/decks && chown -R app:app /data
USER app
COPY --from=build /backend /backend
EXPOSE 8080
ENV PORT=8080
ENV DECK_STORAGE_PATH=/data/decks
ENTRYPOINT ["/backend"]
