# syntax=docker/dockerfile:1

# One process (the app, PID 1) and one SQLite file on /data.

# Test toolchain shared by the image build and scripts/test.sh. gcc and
# musl-dev are only for `-race` (cgo); the SQLite driver is pure Go.
FROM golang:1.27-alpine AS testenv
RUN apk add --no-cache gcc musl-dev

# Tests gate the image: a failing vet or test fails the build.
FROM testenv AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go vet ./...
RUN go test -race ./...
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/voidgrid-shopping ./cmd/voidgrid-shopping

FROM alpine:3.22
# yt-dlp (Alpine package, so the version is fixed by the Alpine release) reads TikTok captions only.
RUN apk add --no-cache ca-certificates yt-dlp
COPY --from=builder /out/voidgrid-shopping /usr/local/bin/voidgrid-shopping
RUN addgroup -g 1000 voidgrid && \
    adduser -D -H -u 1000 -G voidgrid voidgrid && \
    mkdir -p /data && chown 1000:1000 /data
USER 1000:1000
ENV VGS_LISTEN_ADDR=0.0.0.0:8800 \
    VGS_DB_PATH=/data/voidgrid-shopping.db
VOLUME ["/data"]
EXPOSE 8800
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8800/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/voidgrid-shopping"]
