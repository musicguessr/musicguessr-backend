FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod .
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /backend ./cmd/server

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata
RUN addgroup -S app && adduser -S -G app app
USER app
COPY --from=build /backend /backend
EXPOSE 8080
ENV PORT=8080
ENTRYPOINT ["/backend"]
