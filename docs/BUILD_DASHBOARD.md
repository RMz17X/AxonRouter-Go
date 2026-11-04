# 9router-go: Build and Architecture Guide

This document describes how the gateway and its embedded Svelte/Vite
dashboard are built and run. For the architecture itself see
[`../ARCHITECTURE.md`](../ARCHITECTURE.md); for operating the stacks see
[`../README.md`](../README.md); for the database contract see
[`../DATABASE.md`](../DATABASE.md).

Version facts: `VERSION` declares the build version (currently an
experimental `1.9.16-exp.1`); `version.json`'s `latestVersion` is the
stable channel (`1.9.15`). The repository manifest tracks upstream
`v0.5.85`, while the changelog separately lists `v0.5.86` parity items —
not a complete upstream-parity guarantee.

---

## 1. The supported build: Docker

The `Dockerfile` is the build. Three stages:

1. **Web stage** (`oven/bun:1`, on the build platform): `bun install
   --frozen-lockfile` in `web/`, then `bun run build`, producing
   `web/dist`.
2. **Go stage** (`golang:1.27.3-alpine`): copies `web/dist` into place
   and cross-builds the target binary with `CGO_ENABLED=0`; `VERSION`
   is stamped into `internal/updater.CurrentVersion` via `-ldflags`.
3. **Runtime stage** (`alpine:3.21`): the binary runs as a non-root user
   (uid 10001) behind `docker-entrypoint.sh`, which fixes `DATA_DIR`
   volume ownership and drops privileges. The image is ~82 MB and
   health-checks `GET /api/auth/status`.

Build and run it through the compose stacks (the only supported
deployment shape):

```bash
cp .env.example .env   # set POSTGRES_PASSWORD, JWT_SECRET, INITIAL_PASSWORD
docker compose -f docker-compose.single.yml up -d --build
docker compose -f docker-compose.cluster.yml up -d --build   # APP_REPLICAS nodes + Valkey + nginx
```

The release workflow (`.github/workflows/release.yml`) builds this same
Dockerfile for `linux/amd64` + `linux/arm64` and pushes
`luqmenul/9router-go` with channel-derived tags (§4).

## 2. Native build (development)

For a fast edit loop you can build natively — know that the binary then
needs a reachable PostgreSQL at runtime; there is no embedded database
to fall back to.

```bash
make web-build     # bun install (frozen) + bun run build -> web/dist
make build         # stamps VERSION, produces ./9router-go
DATABASE_URL=postgres://user:pass@localhost:5432/9router JWT_SECRET=dev ./9router-go
```

`make dev` runs the same binary via `go run` with `PORT=20130` and an
env file at `~/.9router.env` when present. Go builds need `web/dist` to
exist (it is embedded via `web/embed.go` and gitignored) — build the
dashboard first on a fresh clone.

## 3. Dashboard architecture (unchanged by the platform work)

- **Frontend:** Svelte 5 + Vite + Tailwind in `web/`; the SPA is
  embedded into the Go binary and served by it, with an index fallback
  for client-side routes.
- **Parity discipline:** the dashboard mirrors the upstream (Next.js)
  dashboard's behavior and API contracts; the working record of that
  parity effort is [`DASHBOARD_PROVIDER_PARITY.md`](DASHBOARD_PROVIDER_PARITY.md)
  and [`PARITY-REMAINING.md`](PARITY-REMAINING.md) (both carry status
  banners — read those first).
- **Verification for dashboard changes:** `make vet-svelte` runs
  `svelte-check` as a **ratchet** — the change must not increase the
  error/warning counts over the recorded baseline (AGENTS.md §5.B).

## 4. Versioning and releases

- `scripts/bump-version.sh major|minor|patch [--exp]` updates `VERSION`
  and pushes the git tag; the tag alone determines the release channel
  (a tag containing `-exp` is experimental; the workflow never reads
  `version.json` to decide).
- `version.json` is the **stable channel's** manifest: the release
  workflow rewrites it on stable tags only, so during an experimental
  run it deliberately still names the previous stable version.
- Tagged releases build the five platform binaries (`make cross`),
  checksums, and the multi-arch Docker image; the Telegram notification
  runs after both the binaries and the image exist — see
  [`TELEGRAM_RELEASE_NOTIFICATIONS.md`](TELEGRAM_RELEASE_NOTIFICATIONS.md).

## 5. Historical note

This guide previously described a Bun-compiled standalone dashboard
binary, a greenfield SPA scaffold, and a file-database runtime. All
three are gone: the dashboard has been the embedded Svelte SPA for a
long time, and the runtime is PostgreSQL-only behind the compose
stacks. The KeiRouter port plan that used to be linked from here is
[`keirouter-port-plan.md`](keirouter-port-plan.md), a historical plan
document.
