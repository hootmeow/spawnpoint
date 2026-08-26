# Multi-stage build producing three small runtime images (api, worker,
# admin) from one Go module. Build a specific one with --target, e.g.:
#   docker build --target api    -t spawnpoint-api    .
#   docker build --target worker -t spawnpoint-worker .
# deployments/compose/docker-compose.prod.yml builds api+worker this way.

FROM golang:1.22-alpine AS builder
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ENV CGO_ENABLED=0
RUN go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/spawnpoint-api ./apps/api
RUN go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/spawnpoint-worker ./apps/worker
RUN go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/spawnpoint-admin ./apps/admin

# Shared runtime base: CA certs (for sslmode=require Postgres and any TLS
# upload targets), a non-root user, and a writable data dir for the worker's
# raw-log archive and seen-file cache.
FROM alpine:3.20 AS base
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S spawnpoint && adduser -S spawnpoint -G spawnpoint -h /data && \
    mkdir -p /data/raw-logs /logs && chown -R spawnpoint:spawnpoint /data /logs
WORKDIR /data
USER spawnpoint
ENV ARCHIVE_DIR=/data/raw-logs

FROM base AS api
COPY --from=builder /out/spawnpoint-api /usr/local/bin/spawnpoint-api
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD wget -qO- "http://127.0.0.1:${PORT:-8080}/healthz" >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/spawnpoint-api"]

FROM base AS worker
COPY --from=builder /out/spawnpoint-worker /usr/local/bin/spawnpoint-worker
ENTRYPOINT ["/usr/local/bin/spawnpoint-worker"]
# /logs is the mount point for the game server's log directory (bind-mount
# it there); /data/raw-logs (ARCHIVE_DIR) is the worker's own persistent
# archive and seen-file cache, not the source of logs to watch.
CMD ["-watch", "-interval", "15s", "/logs"]

FROM base AS admin
COPY --from=builder /out/spawnpoint-admin /usr/local/bin/spawnpoint-admin
ENTRYPOINT ["/usr/local/bin/spawnpoint-admin"]
