# migrate-db — the one-shot SQLite → PostgreSQL migrator (P2). A separate
# module with its own go.mod; the SQLite driver lives here and ONLY here
# (the gateway runtime is PostgreSQL-only since P4). Built and run through
# the `migrate` compose profile in docker-compose.single.yml /
# docker-compose.cluster.yml; this Dockerfile's context is this directory.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -ldflags="-s -w" -o /out/migrate-db .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /out/migrate-db /usr/local/bin/migrate-db
ENTRYPOINT ["migrate-db"]
