# AxonRouter-Go Documentation Index

**Version:** `1.9.59`  
**Binary:** `axonrouter-go`  
**Default Port:** `3177`  
**Root Repository:** [`/workspaces/AxonRouter-Go`](file:///workspaces/AxonRouter-Go)

---

## 1. Core Documentation Suite

| Document | Scope |
|---|---|
| [Comprehensive](./COMPREHENSIVE.md) | **Single comprehensive handbook** — complete system architecture, request pipeline (Mermaid), dialect translation, routing, security, operations, and API reference. |
| [Architecture](./ARCHITECTURE.md) | Inbound request pipeline, AST normalization, translation engine, database concurrency, and speed layer topology. |
| [Admin Login](./ADMIN-LOGIN.md) | Dashboard authentication tiers, session cookies, brute-force rate limiting, CVE-2026-56679 prevention, and password resets. |
| [API Reference](./API.md) | Public proxy surface, multi-dialect endpoints (OpenAI, Claude, Gemini, Ollama), SSE streaming, batch APIs, and error envelopes. |
| [Deployment](./DEPLOY.md) | Production deployment topologies: single-node Docker Compose, high-availability cluster (Nginx + 3 nodes), systemd service, and Tailscale tunnels. |
| [Hardening](./HARDENING.md) | Security hardening: client IP trust proxy CIDRs, Argon2id key hashing, AES-256-GCM encryption, guardrails inspection, and pprof isolation. |
| [Operations](./OPERATIONS.md) | Database management (SQLite WAL / PostgreSQL), hot backups, Prometheus metrics scraping, updates, and maintenance routines. |
| [Providers](./PROVIDERS.md) | Supported provider catalog (35+ providers), upstream protocol adaptation, multi-account leasing, OAuth token refresh, and combos. |
| [Quality & Testing](./QUALITY.md) | Code quality standards, static analysis (`.golangci.yml`), test suites, benchmarks, and troubleshooting matrix. |

---

## 2. Engineering Plans & Deep-Dive Specifications

| Document | Topic |
|---|---|
| [Provider Parity Status](./DASHBOARD_PROVIDER_PARITY.md) | Detailed parity status across all 35+ providers vs upstream reference. |
| [Provider Limits Plan](./provider-limits-plan.md) | Per-node provider rate-limiting and quota tracking design. |
| [Smart Combo Plan](./smart-combo-plan.md) | Virtual fallback chains, failover mechanics, and provider routing weights. |
| [KeiRouter Port Plan](./keirouter-port-plan.md) | Porting and compatibility roadmap for KeiRouter features. |
| [Parity Remaining](./PARITY-REMAINING.md) | Audit of remaining upstream parity items and progress. |
| [Antigravity Suspend Analysis](./ANTIGRAVITY_SUSPEND_ANALYSIS.md) | Analysis and mitigation strategies for upstream suspend behaviors. |
| [Specs: Liquid AI Provider](./specs/prd-liquid-ai-provider-and-decision-capabilities.md) | PRD for Liquid AI provider integration and decision capabilities. |

---

## 3. Quick Start

### 3.1 Run via Docker Compose (Single Node)
```bash
docker compose -f docker-compose.single.yml up -d
```
Access dashboard at `http://localhost:3177`. Default login password is `123456` on localhost, or set `INITIAL_PASSWORD`.

### 3.2 Build and Run Locally
```bash
make build
./bin/axonrouter-go
```

---

## 4. Verification & Testing

```bash
# Run unit tests across all packages
make test

# Run linter
make lint

# Run race detection tests
go test -race ./internal/...

# Run performance benchmarks
go test -bench=. ./benchmark/...
```
