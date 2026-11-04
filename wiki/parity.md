---
title: Parity
order: 12
section: Under the Hood
---

# Parity

Both projects share the same database and schema. What differs is the runtime.

## Comparison

| | 9router-go | 9Router (upstream) |
|---|---|---|
| Runtime | Single Go binary | Node.js + Next.js |
| Dashboard | Embedded Svelte 5 | React 19 |
| Database | PostgreSQL (pgx, via `DATABASE_URL`) | SQLite |
| Schema | Same shapes, additive bootstrap | Same |
| Docker | Single binary | Node |
| Port | `20130` | `20128` |

## Guaranteed identical

- Request/response schemas for `/v1/*` — Chat Completions, Messages, Embeddings,
  Audio, Models.
- Dashboard endpoints `/api/*` — Connections, Combos, ProxyPools, Settings,
  ProviderNodes, Usage, Auth.
- Combo expansion, model fallback, account rotation, and strike-breaker quota
  handling.
- Data-shape compatibility: the PostgreSQL schema carries the upstream table/JSON shapes. (The legacy file database is a one-way migration source via `tools/migrate-db`; the products no longer share a live database.)

The two dashboards no longer share a live database: 9router-go moved to
PostgreSQL while upstream keeps its file database. A legacy install's data
crosses over once, through the migration tool.

## What version am I running?

```bash
9router-go version
```

The dashboard's changelog modal shows both `CHANGELOG.md` and any fragment merged
since the last tag, so what you read there is current even between releases.

## Different by design

- Internal implementation leverages Go's performance, strong typing, small
  memory footprint, and idiomatic concurrency — it is not a transliteration of
  JavaScript.
- SSE streams flow directly through `io.Reader`/`io.Writer`, never buffered
  whole.
- Database access: one read per page, never N+1.

## Go-only features

Some features exist only in 9router-go and do not collide with upstream:

- Per-custom-provider limits (RPM/TPM/concurrency/quota/expiry)
- Per-API-key rate limiting with a FIFO queue
- USD + token budgets with hard cutoff
- AES-GCM credential vault
- Argon2id API key hashing
- PII and injection guardrails
- Semantic response cache
- Prometheus metrics

## Further reading

- [`COMPARISON.md`](https://github.com/luqman-v1/9router-go/blob/main/COMPARISON.md)
- [`PARITY-REMAINING.md`](https://github.com/luqman-v1/9router-go/blob/main/docs/PARITY-REMAINING.md)
- Upstream: [decolua/9router](https://github.com/decolua/9router)

For the KeiRouter port specifically — what was taken, what was skipped, and what
was never attempted — see [Advanced Features](advanced-features.html).
