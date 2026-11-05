---
title: Installation
order: 2
section: Getting Started
---

# Installation

## One line

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/RMz17X/AxonRouter-Go/main/install.sh | bash
```

```powershell
# Windows (PowerShell) — writes to %LOCALAPPDATA% + user PATH, no admin needed
irm https://raw.githubusercontent.com/RMz17X/AxonRouter-Go/main/install.ps1 | iex
```

## Defaults

No flags required:

| | Default |
|---|---|
| Port | `20130` |
| Database | PostgreSQL, via `DATABASE_URL` (**required**) |
| State | `DATA_DIR` — `~/.axonrouter-go` (`%APPDATA%/axonrouter-go` on Windows); a pre-rename `~/.9router` is still used when it is the only one that exists; non-database state only |
| Dashboard | `http://localhost:20130` |
| Initial password | `123456` (localhost only) |

## Running

```bash
axonrouter-go              # run in the foreground
curl http://localhost:20130/health
./axonrouter-go version
```

Override only when you need something different (there are no `--port` flags).
A reachable PostgreSQL is mandatory — the binary refuses to boot without
`DATABASE_URL`:

```bash
DATABASE_URL=postgres://user:pass@localhost:5432/axonrouter JWT_SECRET=change-me ./axonrouter-go
PORT=20129 DATABASE_URL=... ./axonrouter-go
DATA_DIR=/srv/axonrouter-go DATABASE_URL=... ./axonrouter-go
HOST=127.0.0.1 DATABASE_URL=... ./axonrouter-go
```

## Running it persistently

The binary's only run mode is the foreground server — the daemon and
service subcommands were removed. For a persistent install use the Docker
stacks below (recommended), or supervise the bare binary with your host's
init system (systemd/launchd/Task Scheduler) yourself.

## Docker

```bash
# from a clone of the repo — the compose stack brings its own PostgreSQL
cp .env.example .env   # set POSTGRES_PASSWORD, JWT_SECRET, INITIAL_PASSWORD
docker compose -f docker-compose.single.yml up -d --build
# dashboard on http://localhost:20130 (or $PORT)
```

Experimental builds are marked **Pre-release** and never offered to a normal
install; Docker Hub tags them `exp` (`luqmenul/axonrouter-go:exp`), never `latest`.

## Building from source

Prerequisites: Go 1.27 and Bun 1.x (the dashboard is embedded into the binary, so build web first).

```bash
git clone https://github.com/RMz17X/AxonRouter-Go.git
cd axonrouter-go
make web-build   # bun install --frozen-lockfile && bun run build
make build       # embeds VERSION into the Go binary
# run it (needs a reachable PostgreSQL):
DATABASE_URL=postgres://user:pass@localhost:5432/axonrouter JWT_SECRET=dev ./axonrouter-go
```

## First login

- **Localhost (`localhost` / `127.0.0.1`)**: the initial compatibility password is
  `123456`. Once logged in, change it in **Settings → Profile**.
- **Remote / VPS / Docker / LAN**: for security (preventing public takeover of
  fresh installs with known defaults), remote access blocks the default `123456`
  password. Either:
  1. Set `INITIAL_PASSWORD` on launch (recommended):
     ```bash
     INITIAL_PASSWORD="your-secure-password" ./axonrouter-go
     ```
  2. Or access via SSH port-forwarding first:
     ```bash
     ssh -L 20130:127.0.0.1:20130 user@remote-host
     # open http://localhost:20130, log in with 123456, then change the password
     ```
