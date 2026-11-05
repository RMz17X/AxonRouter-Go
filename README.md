<div align="center">

# AxonRouter-Go — FREE AI Router & Token Saver (Single Binary)

**Never stop coding. Save 20-40% tokens with RTK + auto-fallback to FREE & cheap AI models.**

**Connect Claude Code, Cursor, Antigravity, Codex, Gemini, OpenCode, Cline, OpenClaw... to 40+ AI providers & 100+ models — no Node.js needed at runtime.**

[![CI](https://github.com/RMz17X/AxonRouter-Go/actions/workflows/ci.yml/badge.svg)](https://github.com/RMz17X/AxonRouter-Go/actions/workflows/ci.yml)
[![Release](https://github.com/RMz17X/AxonRouter-Go/actions/workflows/release.yml/badge.svg)](https://github.com/RMz17X/AxonRouter-Go/actions/workflows/release.yml)
[![GitHub release](https://img.shields.io/github/v/release/RMz17X/AxonRouter-Go)](https://github.com/RMz17X/AxonRouter-Go/releases/latest)
[![License](https://img.shields.io/github/license/RMz17X/AxonRouter-Go)](https://github.com/RMz17X/AxonRouter-Go/blob/main/LICENSE)
[![GitHub All Releases](https://img.shields.io/github/downloads/RMz17X/AxonRouter-Go/total.svg)](https://github.com/RMz17X/AxonRouter-Go/releases)
[![Telegram](https://img.shields.io/badge/Telegram-Join%20Group-26A5E1?logo=telegram&logoColor=white)](https://luqman-v1.github.io/axonrouter-go/tg)

[🚀 Quick Start](#-quick-start) • [💡 Features](#-key-features) • [⚙️ Configuration](#️-configuration) • [🔄 Upgrade from the SQLite era](#-upgrading-from-the-sqlite-era) • [🌐 Upstream](https://github.com/decolua/9router)

</div>

---

## 🤔 Why AxonRouter-Go?

Same idea as [9Router](https://github.com/decolua/9router), minus the Node.js runtime: **one Go binary** serves the proxy APIs + an embedded Svelte dashboard, backed by **PostgreSQL**.

**Stop wasting money, tokens and hitting limits:**

- ❌ Subscription quota expires unused every month
- ❌ Rate limits stop you mid-coding
- ❌ Tool outputs (git diff, grep, ls...) burn tokens fast
- ❌ Manual switching between providers

**axonrouter-go solves this:**

- ✅ **RTK Token Saver** — auto-compress tool_result content, save 20-40% tokens
- ✅ **Auto fallback** — Subscription → Cheap → Free, zero downtime
- ✅ **Extended combo routing** — per-combo strategy: fallback, round-robin, sticky, capacity, or fusion
- ✅ **Multi-account** — round-robin between accounts per provider
- ✅ **Proxy per API key** — bind a proxy pool to each provider API key (connection), one-by-one or via **Apply Proxy** across many at once
- ✅ **Custom headers** — add your own request headers to custom compatible providers
- ✅ **Unified custom provider add** — one shared dialog adds OpenAI-/Anthropic-compatible endpoints
- ✅ **Custom usage ranges** — analyze any window (`14d`, `12h`), not just fixed presets
- ✅ **Extended logging** — live console log with level filters & search, plus per-request payload inspector
- ✅ **Single binary** — Go + embedded dashboard, works with Claude Code, Codex, Cursor, Cline, any CLI tool
- ✅ **Cluster-ready** — run N identical nodes against one PostgreSQL; hot state is shared through Valkey and degrades to PostgreSQL, never to divergent per-node memory

---

## 🔄 How It Works

```text
┌─────────────┐
│  Your CLI   │  (Claude Code, Codex, OpenClaw, Cursor, Cline...)
│   Tool      │
└──────┬──────┘
       │ http://localhost:20130/v1
       ↓
┌─────────────────────────────────────────────┐
│         axonrouter-go (Smart Router)           │
│  • RTK Token Saver (cut tool_result tokens) │
│  • Format translation (OpenAI ↔ Claude)     │
│  • Quota tracking                           │
│  • Auto token refresh                       │
└──────┬──────────────────────────────────────┘
       │
       ├─→ [Tier 1: SUBSCRIPTION] Claude Code, Codex, GitHub Copilot
       │   ↓ quota exhausted
       ├─→ [Tier 2: CHEAP] GLM ($0.6/1M), MiniMax ($0.2/1M)
       │   ↓ budget limit
       └─→ [Tier 3: FREE] Kiro, OpenCode Free, Vertex ($300 credits)

Result: Never stop coding, minimal cost + 20-40% token savings via RTK
```

---

## ⚡ Quick Start

The delivery is **Docker Compose**. Two stacks ship in the repo root:

| Stack | File | Shape |
| --- | --- | --- |
| Single node | `docker-compose.single.yml` | 1 gateway + PostgreSQL 17 |
| Cluster | `docker-compose.cluster.yml` | N gateway replicas + PostgreSQL 17 + Valkey + nginx |

**1. Configure:**

```bash
cp .env.example .env
# Edit .env — at minimum set POSTGRES_PASSWORD, JWT_SECRET and INITIAL_PASSWORD
# to long random values. Every node in a cluster must share the same JWT_SECRET.
```

**2. Start (single node):**

```bash
docker compose -f docker-compose.single.yml up -d
# Dashboard + gateway: http://localhost:20130
```

Log in with the `INITIAL_PASSWORD` from your `.env` (it seeds the dashboard
password on first boot against an empty database), then change it in
**Settings → Profile**.

**Or the cluster:**

```bash
docker compose -f docker-compose.cluster.yml up -d
# nginx publishes the same port; replica count is APP_REPLICAS in .env (default 2)
```

**3. Connect a FREE provider (no signup needed):**

Dashboard → Providers → Connect **Kiro AI** (~50 credits/month free) or **OpenCode Free** (no auth) → Done!

**4. Use in your CLI tool:**

```text
Claude Code / Codex / OpenClaw / Cursor / Cline Settings:
  Endpoint: http://localhost:20130/v1
  API Key:  [Settings → API Keys in the dashboard]
  Model:    kr/claude-sonnet-4.5
```

**That's it!** Start coding with FREE AI models.

> **Upgrading an install from before the PostgreSQL cutover?** Your old
> `data.sqlite` is migrated by the compose `migrate` profile — see
> [Upgrading from the SQLite era](#-upgrading-from-the-sqlite-era). There is
> no shared-database mode anymore: the gateway never opens the legacy file.

📦 [All release binaries](https://github.com/RMz17X/AxonRouter-Go/releases/latest) • 🐳 Image: `luqmenul/axonrouter-go` • 🔨 [Build from source](#-development)

---

## 💡 Key Features

- 🖥️ Native Svelte 5 dashboard: providers, OAuth, combos, proxy pools, API keys, usage, quota, settings, plans, per-entry sidebar visibility
- 💱 Cost display in USD, IDR, EUR and ten more currencies — editable rate table, dual display, one-click refresh from a public provider
- 🔌 OpenAI Chat, Claude Messages, Gemini, Ollama-compatible, Responses, embeddings, media, search, web tools
- 🔁 Combos with fallback, round-robin, sticky routing, fusion, capability-aware reordering
- 👥 Per-provider executors, OAuth refresh, reactive 401 retry
- 📡 Live usage + console-log SSE streams, stall detection
- 🐘 PostgreSQL persistence (partitioned usage ledgers), Valkey speed layer with PostgreSQL degradation, self-update, Docker delivery, cross-compilation

For sequential combo routing, set **Model response timeout** in seconds (decimals
such as `5.3` are fine) in the combo's Edit dialog, or from the compact field on the
combo card. Empty or `0` disables it. The timer covers each model
candidate except the last, including connection selection and waiting for a
usable response. If it expires before output is committed, the router cancels
that candidate and tries the next. The last candidate keeps the gateway's
normal deadline, so the setting never makes a combo fail sooner than it would
without it (#744). An active stream is not cut off by this setting. For
non-streaming requests, the timer covers the wait for the complete response.
Fusion panel timing is separate. A model that times out is left out of that
combo for a
grace period (default 5 minutes, set per combo under the timeout in the Edit
dialog; `0` re-includes it on the next request) and then rejoins the chain. If
every model in the chain is inside its grace period, the full chain is used. The
pause is held in memory and resets on restart. Stored as
`firstTokenGraceMs` next to the timeout. The dashboard shows seconds; the setting is stored in
milliseconds as `comboStrategies[comboName].firstTokenTimeoutMs`.

---

## 📸 Screenshots

![axonrouter-go dashboard — Providers view](docs/screenshots/providers.png)

![axonrouter-go dashboard — Endpoint & API key setup](docs/screenshots/endpoint.png)

---

## ⚙️ Configuration

Configuration is by **environment variable** — there are no config files to
edit and no database path to set. The compose stacks read `.env`;
[`.env.example`](.env.example) is the annotated contract. A bare binary reads
the same variables from its own environment.

| Variable | Required | Purpose |
| --- | --- | --- |
| `DATABASE_URL` | **yes** | PostgreSQL connection string, e.g. `postgres://user:pass@host:5432/axonrouter?sslmode=disable`. A boot without it is a fatal error — there is no embedded or file database in this build. The compose stacks assemble it from `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB`. |
| `JWT_SECRET` | **yes** (in practice) | Signs dashboard sessions/tokens. Every cluster node must share one value. If unset, the gateway generates an ephemeral per-boot secret and logs a loud warning: sessions then survive neither a restart nor a second node. |
| `INITIAL_PASSWORD` | first boot | Seeds the dashboard password against an empty database. Change it from the dashboard afterwards. Without it, only local access can use the temporary compatibility default; remote login is refused until a password is set. |
| `PORT` | no (default `20130`) | HTTP port. In the cluster stack, nginx publishes it. |
| `HOST` | no | Listener address; bind `127.0.0.1` to serve localhost only (e.g. behind your own reverse proxy). |
| `DATA_DIR` | no | **Non-database** state root: CLI tokens/machine-id, uploads, provider instruction files, tunnel state, catalog cache. In compose it is the `appdata` volume at `/data`. The database never lives here. |
| `VALKEY_URL` / `REDIS_URL` | no | Speed layer: share hot state (in-flight counters, rotation cursors, model locks) across nodes. `VALKEY_URL` wins. Unset = per-node memory (single-node mode). A configured-but-dead server degrades the gateway to PostgreSQL — never to divergent per-node memory. |
| `NODE_ID` | no | Node identity in boot logs and status payloads (default: hostname). |
| `PG_POOL_MAX_OPEN` / `PG_POOL_MAX_IDLE` | no (defaults 10 / 5) | PostgreSQL connection pool per node, sized for small hardware and a shared `max_connections` budget. |
| `SHUTDOWN_GRACE_SECONDS` | no (default `30`) | Drain window for in-flight streams on shutdown; the compose stacks pair it with an equal `stop_grace_period`. |
| `RTK_ENABLED` / `CAVEMAN_ENABLED` / `PONYTAIL_ENABLED` | no | Token-saver toggles (RTK defaults on). |
| `HTTP_PROXY` / `HTTPS_PROXY` | no | Upstream egress proxy. |

OAuth client overrides, `FORCE_FALLBACK_ON_ALL_UNAVAILABLE`, and `STRICT_SSL`
are documented in `.env.example`.

### First login

- **Compose / remote / VPS:** set `INITIAL_PASSWORD` in `.env` before the
  first boot (recommended above). Remote access blocks the well-known
  compatibility default password, so a fresh public install cannot be taken
  over with a known password (CVE-2026-56679).
- **Bare binary on your own machine:** local login can use the temporary
  compatibility password `123456` until you set a real one in
  **Settings → Profile** — do that first.

### API surface

```text
POST /v1/chat/completions       OpenAI Chat Completions
POST /v1/messages               Claude Messages
POST /v1/responses              Responses API
POST /v1/embeddings             Embeddings
POST /api/chat                  Ollama-compatible chat
GET  /v1/models                 Model catalog
GET  /v1/models/{kind}          Catalog filtered by kind (image, tts, stt, embedding, ...)
GET  /v1/models/info?id=...     One model's catalogue entry
POST /v1/images/generations     Image generation
POST /v1/videos/generations     Video generation
GET  /v1/videos/{id}            Video job status (proxied from the creating connection)
GET  /v1/videos/{id}/content    Video job content
POST /v1/audio/speech           Text to speech
GET  /v1/audio/voices           TTS voices (provider whitelist)
POST /v1/audio/transcriptions   Speech to text
POST /v1/search                 Web search
GET  /api/usage/stream          Live usage SSE
GET  /health                    Liveness
GET  /api/version               Version metadata
```

For Claude Messages clients: `ANTHROPIC_BASE_URL=http://localhost:20130/v1`.

Client example:

```bash
curl http://localhost:20130/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer sk-your-api-key' \
  -d '{"model":"ag/gemini-3.8-flash-high","messages":[{"role":"user","content":"Hello"}],"stream":true}'
```

### Authentication

- Public: `/health`, dashboard HTML/assets, `/login`, OAuth callbacks.
- Proxy routes need an active client API key (`Authorization: Bearer` / `X-API-Key`).
- Dashboard APIs need a session cookie, CLI token, or API key; destructive ops (shutdown, update, DB import) need a session or CLI token.
- The API server defaults to all interfaces — bind localhost or protect the port outside trusted machines.

### Gemini Web session renewal

The `gemini-web` provider reaches `gemini.google.com` with a session cookie,
and Google rotates the session's timestamp cookie (`__Secure-1PSIDTS`) every
few minutes. The router renews it in the background, so a signed-in account
keeps reaching the models it pays for instead of falling back to one model
after a few minutes.

**There are three ways to get a session, and the dashboard offers two of
them as a button.**

1. *A throwaway browser* — the safest. The button opens a browser with a
   profile of its own, waits for the Google sign-in, reads the session out of
   it, then closes the browser and deletes the profile. Nothing is left
   running that could rotate the session, so the router's renewal is the
   only one.
2. *A browser that is already signed in* — the convenient one. The button
   opens the browser's own cookie database, takes the key that seals it from
   the platform's secret store (the login keyring on Linux, the Keychain on
   macOS, the profile's "Local State" file on Windows), and unseals the
   cookies. **A browser that is still open keeps rotating the same session**
   and the router renews it from the other side, and the two cancel each
   out — which signs the account out. Close the browser first, or use the
   throwaway browser above. The dashboard says so when it finds a browser
   that is running.
3. *A cookie you paste* — the fallback. Copy the cookie header from an
   Incognito window that you close right after, not from a normal tab, and
   paste it into the connection form.

Set `GEMINI_WEB_ROTATION=off` to run the router without renewing anything at
all. That is the right choice when the credential belongs to a browser you
want to keep using.

### Trusted proxies and client IPs

A forwarded client-IP header is only read when the TCP peer is inside the
trusted set — otherwise a client could rotate `X-Forwarded-For` and give every
login attempt its own rate-limit bucket. `TRUST_PROXY=true` trusts loopback and
the RFC1918/link-local ranges; `TRUSTED_PROXIES` takes a comma-separated list
of CIDRs or single IPs and **replaces** those defaults, so set it when the
proxy connects from an address outside them.

Behind a proxy that is *not* in the trusted set, every request is bucketed by
the proxy's own socket address, so all users of that proxy share one login
bucket: five failed attempts lock them all out. `TRUSTED_PROXIES` is the knob
that separates them again. (The cluster stack's nginx is inside the compose
network; a proxy you add in front of the stack is what this setting is for.)

---

## 🔄 Upgrading from the SQLite era

Installs from before the PostgreSQL cutover keep their data in a legacy
`data.sqlite`. The gateway no longer opens that file — a one-shot migrator
(`tools/migrate-db`, a separate module and image) copies it into PostgreSQL
and verifies every table (`target = source − skipped`, with malformed rows
skipped and reported, never silently mangled).

With either compose stack:

```bash
# 1. Put the legacy file in the legacy directory (default ./legacy,
#    override with LEGACY_DIR in .env):
mkdir -p legacy && cp /path/to/old/data.sqlite legacy/

# 2. Run the migrate profile BEFORE the first app boot on the new database:
docker compose -f docker-compose.single.yml --profile migrate run --rm migrate

# 3. Start the stack normally:
docker compose -f docker-compose.single.yml up -d
```

Notes that matter:

- The migrator **refuses a non-empty target** unless `--truncate` is added to
  its command — an accidental second run cannot silently merge two eras.
- The legacy directory is mounted **writable** on purpose. The migrator opens
  the database read-only (the file itself is never written — checksum-verified),
  but a WAL-mode source needs a writable directory for its `-shm`/`-wal`
  recovery sidecars; a read-only mount fails the open.
- Row ids, timestamps, and JSON payloads are preserved exactly; the monthly
  partitions for the months present in the source are created for you.
- Upstream 9Router's own database file is the same legacy format — the same
  profile migrates it. After migration there is **no** going back to a shared
  file: 9Router (Next.js) cannot read PostgreSQL.

Schema, partitioning, backup/restore, and the degradation model:
[`DATABASE.md`](DATABASE.md). Routing internals: [`ARCHITECTURE.md`](ARCHITECTURE.md).

---

## 🔨 Development

Prerequisites: Go 1.27, Bun 1.x, and a PostgreSQL 17 to point `DATABASE_URL`
at (the compose stacks provide one; for a bare run any reachable server
works — the gateway creates its schema on first boot).

```bash
git clone https://github.com/RMz17X/AxonRouter-Go.git
cd axonrouter-go

# The supported dev loop is compose-based (same stacks as production):
cp .env.example .env   # set POSTGRES_PASSWORD / JWT_SECRET / INITIAL_PASSWORD
docker compose -f docker-compose.single.yml up -d

# Frontend hot-reload against a running gateway:
make web-dev           # Vite dev server on :5173, API proxied to :20130

# Native build (embeds the dashboard; needs web/dist first):
make web-build
make build
DATABASE_URL=postgres://user:pass@localhost:5432/axonrouter?sslmode=disable \
JWT_SECRET=dev-secret ./axonrouter-go
./axonrouter-go version
```

The CLI is deliberately small — the binary's only run mode is the foreground
server (on a TTY it first shows a short menu):

```text
axonrouter-go            start the gateway in the foreground
axonrouter-go version    print version info
axonrouter-go update     check for and install a self-update
axonrouter-go help       show usage
```

There are no daemon/service subcommands: process supervision belongs to
Docker (or your init system) now.

Before writing code, skim [`AGENTS.md`](AGENTS.md) — provider isolation and
the Go/Svelte conventions there are enforced by review, and the changelog
discipline (§7.6: a `.changes/` fragment per PR, never an edit to
`CHANGELOG.md`) is mechanical.

**A note on the test fleet.** The legacy suites that were written against
the removed file database are quarantined behind the `sqlite_legacy` build
tag (decision D1) and the `integration`-tagged suite's harness predates the
cutover; neither runs in the default build. Verification for new work is
`go build ./...`, `go vet ./...`, `make vet-svelte` for dashboard changes,
and live probes against a running gateway. See AGENTS.md §5.

### Experimental builds

Unreleased work also ships as **experimental** builds. They are published
separately and are never offered to a normal install:

- GitHub Releases marks them **Pre-release**, so `releases/latest` — the URL
  `axonrouter-go update` falls back to — keeps pointing at the stable version.
- Docker Hub tags them `exp` (`luqmenul/axonrouter-go:exp`), never `latest`.

```bash
docker pull luqmenul/axonrouter-go:exp
```

Opt in only if you want to help shake out bugs. `axonrouter-go update` will not
install one over a stable release — grab the asset for your platform from the
[pre-releases page](https://github.com/RMz17X/AxonRouter-Go/releases) and
replace the running binary with it.

---

## 🤝 Contributing

Issues and pull requests use templates, so pick the right one rather than opening a blank report:

- **Bug report** — something behaves incorrectly. Include a reproduction, `axonrouter-go version`, OS, and the log excerpt around the failure.
- **Feature request** — the problem you cannot solve today, the surface it touches, and whether upstream already has it.
- **Upstream parity** — a behaviour `decolua/9router` has and this gateway does not; link the upstream commit or PR.
- **Question** — configuration and usage help.

PRs target `main` (AGENTS.md §7.7) and carry a `.changes/` fragment instead
of a `CHANGELOG.md` edit. The checks that gate a change: `go vet ./...`,
`go build ./...`, and for dashboard work `make vet-svelte` plus a live
check against a running gateway.

Everyone whose commit landed in the repository is credited on the wiki's [**Owner & Contributors**](https://luqman-v1.github.io/axonrouter-go/team.html) page — photo, username, and contribution count, generated from the GitHub API.

## 📚 Docs

**Full documentation, searchable:** [**axonrouter-go Wiki**](https://luqman-v1.github.io/axonrouter-go/) — installation, usage, API reference, dashboard, providers, architecture, database, CLI, and a screenshot gallery. Every page supports `?q=` search and `#section` deep links.

Repository references:

- [`ARCHITECTURE.md`](ARCHITECTURE.md) — routing, providers, data layer, speed layer, cluster topology
- [`DATABASE.md`](DATABASE.md) — PostgreSQL schema, partitioning, backup/restore, degradation model
- [`ROADMAP.md`](ROADMAP.md) — proposals only, not current behavior
- [`CHANGELOG.md`](CHANGELOG.md) — release history (Go **v1.9.15**, upstream baseline `decolua/9router`). Work merged since the last tag lives in [`.changes/`](.changes/) and is folded in at the next release; the dashboard's changelog modal shows both. Check what you are running with `axonrouter-go version`.
- [`docs/TELEGRAM_RELEASE_NOTIFICATIONS.md`](docs/TELEGRAM_RELEASE_NOTIFICATIONS.md) — one-time setup for the release notice posted to Telegram

## Credits

- [9Router](https://github.com/decolua/9router) — original Next.js gateway & dashboard this Go port preserves compatibility with

## 📄 License

MIT License — lihat [`LICENSE`](https://github.com/RMz17X/AxonRouter-Go/blob/main/LICENSE).
