# AxonRouter-Go: Operations & Maintenance Guide

**Target Version:** `1.9.59`  
**Operational References:** [`wiki/operations.md`](file:///workspaces/AxonRouter-Go/wiki/operations.md), [`DATABASE.md`](file:///workspaces/AxonRouter-Go/DATABASE.md), [`internal/db/`](file:///workspaces/AxonRouter-Go/internal/db/)

---

## 1. Database Architecture & Persistence

AxonRouter-Go operates with either **SQLite** (single node default) or **PostgreSQL 17** (cluster mode):

### 1.1 SQLite Configuration
- **Location:** `$DATA_DIR/axonrouter.db`
- **Journal Mode:** `WAL` (Write-Ahead Logging) enabled automatically for concurrent read performance.
- **Busy Timeout:** Configured to `5000ms` to avoid lock contention during heavy burst traffic.

### 1.2 PostgreSQL Configuration
- **Connection URI:** Configured via `DATABASE_URL=postgres://user:pass@host:5432/axonrouter?sslmode=disable`
- **Driver:** Native `pgx` pool with automatic connection reuse, health check ping, and statement preparation caching.
- **Migrations:** Applied automatically at gateway boot via embedded schema migration scripts in `internal/db/migrations/`.

---

## 2. Backup & Disaster Recovery

### 2.1 SQLite Backup
To create a consistent hot backup of an active SQLite database without stopping the router:
```bash
# Using sqlite3 online backup API
sqlite3 /data/axonrouter.db ".backup '/data/backup-$(date +%Y%m%d%H%M%S).db'"
```

### 2.2 PostgreSQL Backup & Restore
```bash
# Backup
pg_dump -Fc -v -h localhost -U axon axonrouter > /backup/axonrouter_$(date +%F).dump

# Restore
pg_restore -v -d axonrouter -h localhost -U axon /backup/axonrouter_2026-11-09.dump
```

---

## 3. Observability & Monitoring

### 3.1 Health Check Endpoints
- **Liveness:** `GET /health` returns `200 OK` with `{"status":"ok"}`. Ideal for container liveness probes.
- **Readiness / Version:** `GET /api/version/status` reports detailed version, database connectivity, and active uptime.

### 3.2 Prometheus Metrics Scraping
When enabled, Prometheus metrics are served at `GET /metrics`:
- `axonrouter_requests_total{model="...", provider="...", status="..."}`: Total request counts.
- `axonrouter_request_duration_seconds{model="...", provider="..."}`: Histogram of request latencies.
- `axonrouter_tokens_total{direction="input|output", provider="..."}`: Processed token volume.
- `axonrouter_provider_active_leases{provider="..."}`: Active concurrent account leases.

---

## 4. Lifecycle Management & Updates

### 4.1 Update Mechanisms
AxonRouter-Go includes integrated update capabilities:
- **Version Check:** `GET /api/version/check` queries GitHub releases for newer versions.
- **Trigger Update:** `POST /api/version/update` downloads the target release binary, validates checksums, replaces the binary atomically, and restarts the process.
- **Auto-Update Toggle:** `POST /api/version/auto-update` enables or disables automated background upgrades.

### 4.2 Graceful Shutdown
Trigger graceful termination via:
```bash
curl -X POST http://localhost:3177/api/version/shutdown \
  -H "Cookie: axonrouter_session=..."
```
This stops accepting new ingress requests, waits for in-flight streams to complete (up to 30s deadline), flushes pending usage ledgers, and cleanly disconnects database pools.

---

## 5. Background Loops & Maintenance Routines

1. **Auto-Ping Scheduler:** Periodically sends keep-alive probes through configured providers to verify credential validity and warm up connection pools.
2. **Catalog Sync Worker:** Synchronizes remote model lists and pricing tables from upstream repositories.
3. **Usage Rollup Engine:** Batches individual request completion tokens into hourly and daily ledger tables for fast dashboard rendering.
