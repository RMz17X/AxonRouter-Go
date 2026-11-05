---
title: CLI
order: 18
section: Using the Gateway
---

# CLI

Every command comes from the single binary.

## Running the gateway

```bash
axonrouter-go                  # foreground server (port 20130) — the only run mode
axonrouter-go version          # version details, plus an update check
axonrouter-go update           # check and self-update
curl http://localhost:20130/health
```

## Supervision

There are no daemon or service subcommands anymore: the binary runs in the
foreground, and supervision belongs to the deployment — the Docker compose
stacks (`docker-compose.single.yml` / `docker-compose.cluster.yml`) for
containers, or the host's init system for a bare binary. Removed commands
(`start`, `stop`, `restart`, `status`, `logs`, `service`, `mitm`) answer
`unknown command`.

## Interactive menu

Running `axonrouter-go` with no arguments in a terminal opens a menu:

```text
1) Start in this terminal (foreground)
2) Run in background with tray icon (frees the terminal)
0) Exit
```

**Option 2 — tray.** The tray icon lives in a companion binary
(`cmd/axonrouter-tray`), not the main one: a GUI toolkit cannot be linked into the
gateway without breaking the `CGO_ENABLED=0` cross-build for every target. Only
the *sibling* binary is tried, so a PATH lookup cannot pick up another install's
tray talking to a different gateway. Right-click the icon to open the dashboard
or quit.

```bash
# the tray ships as its own package
axonrouter-tray --port 20130
```

## Flags

There are no `--port` flags; use environment variables.

### Server

| Variable | Default | Effect |
|---|---|---|
| `PORT` | `20130` | HTTP port |
| `DATA_DIR` | `~/.axonrouter-go` (`%APPDATA%/axonrouter-go` on Windows); a pre-rename `~/.9router` is still used when it is the only one that exists | Non-database state root (uploads, CLI tokens, tunnel state, catalog cache) |
| `DATABASE_URL` | — (**required**) | PostgreSQL connection URL; the server refuses to boot without it |
| `HOST` / `BIND_ADDR` | all interfaces | Listener address |
| `INITIAL_PASSWORD` | unset (`123456` on localhost) | First dashboard password |
| `API_KEY_SECRET` | built-in default | API key hashing secret |
| `MACHINE_ID_SALT` | built-in default | Machine identity salt |
| `JWT_SECRET` | ephemeral per-boot secret when unset (loud warning) | Session signing secret — env-only; set it in production so restarts and extra nodes do not invalidate sessions |
| `AUTH_COOKIE_SECURE` | unset | Set the `Secure` flag on session cookies (behind TLS) |
| `FX_LOGGING` | unset | Log Fx dependency-injection events |
| `NINEROUTER_PEER_TOKEN` | unset | Peer token for internal routing |

### Token savers

| Variable | Default | Flag |
|---|---|---|
| `RTK_ENABLED` | `true` | `--rtk=false` |
| `CAVEMAN_ENABLED` | `false` | `--caveman=true` |
| `PONYTAIL_ENABLED` | `false` | `--ponytail=true` |
| `ADHD_ENABLED` | `false` | `--adhd=true` |
| `INJECTION_GUARD_DISABLED` | `false` | `--no-injection-guard` |
| `AUTO_UPDATE` | `false` | `--auto-update=true` |

A flag overrides the stored dashboard setting for that run only.

### Networking and trust

| Variable | Default | Effect |
|---|---|---|
| `HTTP_PROXY` / `HTTPS_PROXY` | Go defaults | Upstream egress proxy |
| `TRUST_PROXY` | unset | Trust loopback + RFC1918/link-local forwarded IPs |
| `TRUST_CLOUDFLARE` | unset | Trust Cloudflare forwarded IPs |
| `TRUSTED_PROXIES` | unset | CIDR/IP list that **replaces** the defaults |
| `PPROF_ENABLED` | `false` | Expose `/debug/pprof/*` |
| `GEMINI_WEB_ROTATION` | `on` | Renew the Gemini Web session cookie in the background |
| `FORCE_FALLBACK_ON_ALL_UNAVAILABLE` | unset | When every account of a provider is in cooldown, serve through whichever frees up first |
| `STRICT_SSL` | unset | Set true/1 to forbid the insecure-TLS retry |

### OAuth overrides

Built-in public client credentials exist for antigravity, gemini-cli, xai, codex,
iflow and others, so most operators never set these. Override per provider:

```bash
ANTIGRAVITY_OAUTH_CLIENT_ID=...
ANTIGRAVITY_OAUTH_CLIENT_SECRET=...
CODEX_OAUTH_CLIENT_ID=...
```

See `.env.example` for the full list.

## Verifying a release

Download from [GitHub Releases](https://github.com/RMz17X/AxonRouter-Go/releases/latest)
and verify against `SHA256SUMS.txt`.
