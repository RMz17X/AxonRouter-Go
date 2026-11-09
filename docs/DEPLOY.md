# AxonRouter-Go: Production Deployment Guide

**Version:** `1.9.59`  
**Deployment Configurations:** [`docker-compose.single.yml`](file:///workspaces/AxonRouter-Go/docker-compose.single.yml), [`docker-compose.cluster.yml`](file:///workspaces/AxonRouter-Go/docker-compose.cluster.yml), [`Dockerfile`](file:///workspaces/AxonRouter-Go/Dockerfile)

---

## 1. Deployment Topologies

AxonRouter-Go supports three primary deployment modes:

1. **Single Container / Standalone (Standard):** Single binary container running with SQLite or dedicated PostgreSQL.
2. **High-Availability Cluster:** 3 backend Go router nodes load-balanced behind an embedded Nginx reverse proxy with shared PostgreSQL.
3. **Bare-Metal / Systemd:** Direct binary execution on Linux hosts.

---

## 2. Single-Node Docker Compose

Recommended for developer instances, edge nodes, and small-to-medium workloads:

```yaml
version: "3.8"

services:
  axonrouter:
    image: luqmenul/axonrouter-go:latest
    container_name: axonrouter-go
    restart: unless-stopped
    ports:
      - "20130:20130"
    volumes:
      - axonrouter_data:/data
    environment:
      - PORT=20130
      - DATA_DIR=/data
      - INITIAL_PASSWORD=change_this_secure_password
      - TRUST_PROXY=true
      - PPROF_ENABLED=false
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:20130/health"]
      interval: 15s
      timeout: 5s
      retries: 3

volumes:
  axonrouter_data:
```

Launch with:
```bash
docker compose -f docker-compose.single.yml up -d
```

---

## 3. High-Availability Cluster Deployment

For production traffic requiring zero-downtime rolling updates and failover:

```mermaid
flowchart TD
  Client[Ingress Traffic] --> Nginx[Nginx Load Balancer: Port 20130]
  Nginx --> Node1[axonrouter-node-1: 20131]
  Nginx --> Node2[axonrouter-node-2: 20132]
  Nginx --> Node3[axonrouter-node-3: 20133]

  Node1 --> Postgres[(PostgreSQL 17 Database)]
  Node2 --> Postgres
  Node3 --> Postgres

  Node1 --> Speed[Shared Speed Layer / Valkey]
  Node2 --> Speed
  Node3 --> Speed
```

Launch with:
```bash
docker compose -f docker-compose.cluster.yml up -d
```

Key features of the cluster configuration:
- **Upstream Health Checks:** Nginx automatically bypasses degraded or restarting nodes.
- **Shared Lease Table:** Node concurrency and provider leases coordinate through PostgreSQL locks and Valkey.
- **WebSocket / SSE Support:** Nginx configuration explicitly sets `proxy_buffering off;` and passes `Connection "upgrade"` to preserve streaming responsiveness.

---

## 4. Bare Metal / Systemd Deployment

### 4.1 Build Binary from Source
```bash
make build
# outputs binary to ./bin/axonrouter-go
```

### 4.2 Systemd Service Unit (`/etc/systemd/system/axonrouter.service`)
```ini
[Unit]
Description=AxonRouter-Go LLM Proxy Gateway
After=network.target postgresql.service

[Service]
Type=simple
User=axonrouter
Group=axonrouter
WorkingDirectory=/opt/axonrouter
ExecStart=/opt/axonrouter/axonrouter-go
Restart=always
RestartSec=5s
LimitNOFILE=65536

Environment=PORT=20130
Environment=DATA_DIR=/opt/axonrouter/data
Environment=DATABASE_URL=postgres://axon:secret@127.0.0.1:5432/axonrouter?sslmode=disable
Environment=INITIAL_PASSWORD=supersecretadminpassword
Environment=TRUST_PROXY=true

[Install]
WantedBy=multi-user.target
```

Enable and start:
```bash
systemctl daemon-reload
systemctl enable --now axonrouter
```

---

## 5. Tailscale Tunnel Integration

AxonRouter-Go includes built-in Tailscale support to expose the dashboard without opening public firewall ports:

1. When running in Docker, ensure container capabilities include:
   ```yaml
   cap_add:
     - NET_ADMIN
   devices:
     - /dev/net/tun:/dev/net/tun
   ```
2. Enable Tailscale tunnel via dashboard: **Settings → Network → Tailscale Tunnel** or through API `POST /api/tunnel/tailscale-enable`.

---

## 6. Pre-flight Verification Checklist

- [ ] `INITIAL_PASSWORD` is configured (prevents CVE-2026-56679 remote lockout).
- [ ] `LimitNOFILE` is set to at least `65536` for high concurrency SSE connections.
- [ ] Reverse proxy disables buffering (`X-Accel-Buffering: no` and `proxy_buffering off`).
- [ ] Healthcheck responds `{"status":"ok"}` on `GET /health`.
