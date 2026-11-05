# Stage 1: Build Frontend SPA
# Build on BUILDPLATFORM (native host architecture) to avoid emulating Bun under QEMU.
# Output in web/dist is static HTML/JS/CSS, architecture-agnostic, and shared across targets.
FROM --platform=$BUILDPLATFORM oven/bun:1-alpine AS web-builder
WORKDIR /app/web
COPY web/package.json web/bun.lock ./
RUN --mount=type=cache,target=/root/.bun/install/cache bun install --frozen-lockfile
COPY web/ ./
RUN bun run build

# Stage 2: Build Go Binary with Embedded Assets
# Build on BUILDPLATFORM using Go's native cross-compiler (CGO_ENABLED=0 GOOS/GOARCH)
# instead of running the Go toolchain inside slow QEMU software emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web-builder /app/web/dist ./web/dist

# Automatic Buildx multi-platform arguments
ARG TARGETOS
ARG TARGETARCH
# Central version — single source is VERSION file (and version.json for runtime)
ARG VERSION
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    VERSION=${VERSION:-$(cat VERSION 2>/dev/null || cat version.json | sed -n 's/.*"latestVersion": *"\([^"]*\)".*/\1/p')} && \
    echo "Building version $VERSION for ${TARGETOS:-linux}/${TARGETARCH:-amd64} on $BUILDPLATFORM" && \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -ldflags="-s -w -X 'axonrouter/proxy/internal/updater.CurrentVersion=${VERSION}'" -o axonrouter-go ./cmd/axonrouter-go/

# Stage 3: Minimal runtime image for target platform.
# The gateway is PostgreSQL-only (P4): the runtime needs DATABASE_URL,
# JWT_SECRET and PORT — no database files, no sqlite anything. DATA_DIR
# remains as the non-DB state root (CLI tokens, uploads, tunnel state,
# catalog cache) and is a named volume in the compose stacks.
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata su-exec \
    && addgroup -S -g 10001 app \
    && adduser -S -u 10001 -G app app \
    && mkdir -p /data \
    && chown app:app /data
COPY --from=builder /app/axonrouter-go /usr/local/bin/axonrouter-go
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh
ENV PORT=3177 \
    DATA_DIR=/data \
    AXONROUTER_IN_CONTAINER=1
EXPOSE 3177
# The entrypoint hands the state volume to the app user, then drops
# privileges — the server process itself always runs as uid 10001.
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["axonrouter-go"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -q -O /dev/null "http://127.0.0.1:${PORT:-3177}/api/auth/status" || exit 1
