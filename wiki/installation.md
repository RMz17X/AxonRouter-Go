---
title: Installation
order: 2
section: Getting Started
---

# Installation

AxonRouter-Go is delivered as **Docker Compose** stacks — that is the
only supported way to run it.

## Single node

One gateway + PostgreSQL 17:

```bash
# from a clone of the repo — the compose stack brings its own PostgreSQL
cp .env.example .env   # set POSTGRES_PASSWORD, JWT_SECRET, INITIAL_PASSWORD
docker compose -f docker-compose.single.yml up -d
# dashboard on http://localhost:20130 (or $PORT)
```

## Cluster

N gateway replicas + PostgreSQL 17 + Valkey + nginx. The replica count
is `APP_REPLICAS` in `.env` (default 2), and every node must share the
same `JWT_SECRET`:

```bash
cp .env.example .env   # set POSTGRES_PASSWORD, JWT_SECRET, INITIAL_PASSWORD
docker compose -f docker-compose.cluster.yml up -d
# nginx publishes the same port
```

## Defaults

| | Default |
|---|---|
| Port | `20130` |
| Database | PostgreSQL 17, provided by the stack |
| State | the `appdata` volume at `/data` inside the container — non-database state only |
| Dashboard | `http://localhost:20130` |
| Initial password | the `INITIAL_PASSWORD` from your `.env` |

Experimental builds are marked **Pre-release** and never offered to a
normal install; Docker Hub tags them `exp` (`luqmenul/axonrouter-go:exp`),
never `latest`.

## Updates

Update by pulling the new image and recreating the stack
(`docker compose ... up -d` on the new image). The gateway's
self-updater is disabled inside containers: the container marker
`AXONROUTER_IN_CONTAINER=1`, set by the image and both compose stacks,
turns the background check off and makes the dashboard update actions
and `axonrouter-go update` refuse with an explanation.

## First login

- Set `INITIAL_PASSWORD` in `.env` before the first boot; it seeds the
  dashboard password against an empty database. Log in, then change it
  in **Settings → Profile**.
- The fallback compatibility password is `123456`, accepted on
  localhost only. Remote access blocks it, which is what prevents a
  fresh install with a known default password from being taken over.

## Running outside Docker (unsupported)

The gateway binary will still start on a bare host, but that is
unsupported DIY — there is no installer script and no supported
host-install path. You provide your own **PostgreSQL 17**: a reachable
`DATABASE_URL` is mandatory and the binary refuses to boot without one.
For cluster mode you also provide your own **Valkey** (`VALKEY_URL`).
Non-database state lives in `DATA_DIR` (`~/.axonrouter-go` by default;
a pre-rename `~/.9router` is still used when it is the only one that
exists). Supervision by your host's init system and binary updates (the
self-updater stays enabled outside containers) are yours to arrange.

Building the binary from source is a development activity — see the
repo README's Development section.
