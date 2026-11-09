# AxonRouter-Go: Security Hardening Guide

**Target Version:** `1.9.59`  
**Security References:** [`wiki/security.md`](file:///workspaces/AxonRouter-Go/wiki/security.md), [`internal/middleware/auth.go`](file:///workspaces/AxonRouter-Go/internal/middleware/auth.go), [`internal/auth/session.go`](file:///workspaces/AxonRouter-Go/internal/auth/session.go)

---

## 1. Client IP & Trust Proxy Clamping

To prevent IP spoofing that could bypass rate limiters and login lockout buckets, AxonRouter-Go enforces strict proxy trust boundaries:

| Variable | Default | Security Behavior |
|---|---|---|
| `TRUST_PROXY` | `false` | When `true`, trusts loopback (`127.0.0.1`, `::1`) and RFC1918 private subnets (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`). |
| `TRUST_CLOUDFLARE` | `false` | When `true`, validates and trusts Cloudflare proxy IP headers (`CF-Connecting-IP`). |
| `TRUSTED_PROXIES` | `""` | Comma-separated list of explicit CIDRs/IPs. Overrides default ranges when specified. |

### Danger of Misconfiguration:
If `TRUST_PROXY` is enabled without being behind a trusted reverse proxy, untrusted external clients can inject arbitrary `X-Forwarded-For` headers to cycle IP addresses and evade brute-force lockout. Behind an untrusted proxy, AxonRouter-Go falls back to bucketing by the physical TCP socket address.

---

## 2. Credential Encryption & Hashing Standards

AxonRouter-Go implements zero plaintext storage for sensitive credentials:

### 2.1 Client API Keys
- **Hashing Algorithm:** **Argon2id** (memory-hard, resistant to GPU/ASIC brute forcing).
- **Lookup Index:** Keys use a separate **SHA-256** hash index for $O(1)$ fast lookups in the database without exposing raw keys or computing Argon2id on every inbound request.

### 2.2 Provider Credentials & Tokens
- **Encryption Algorithm:** **AES-256-GCM** authenticated encryption.
- **Master Key:** Derived from `APP_SECRET` / `ENCRYPTION_KEY` using HKDF-SHA256.
- **Stored Attributes:** Nonce and ciphertext stored together; tamper detection built-in via GCM tag verification.

---

## 3. Rate Limiting & Concurrency Ceiling

To shield upstream provider accounts and prevent runaway billing:
1. **Sliding Window:** Tracks RPM (Requests per minute) and TPM (Tokens per minute) in a sliding time window.
2. **Concurrency Limiter:** Bounds the number of simultaneous in-flight HTTP connections per client key.
3. **Provider Node Limiter:** Separate limiter protecting individual upstream provider accounts from being throttled by provider-side 429s.

---

## 4. Inbound Guardrails & Content Inspection

AxonRouter-Go integrates an inline guardrail inspection engine before dispatching requests upstream:
- **Prompt Injection Defense:** Scans user prompts for system prompt override vectors (e.g. `Ignore previous instructions and do X`).
- **Secret Redaction:** Detects AWS, GitHub, OpenAI, and SSH private keys in prompt bodies to prevent accidental credential exfiltration.
- **Fail-Safe Switch:** Guardrails can be dynamically disabled via `repo.GetGuardrailsEnabled()` without requiring container restart.

---

## 5. Session & Cookie Hardening

- **Cookie Flags:** `HttpOnly: true` (inaccessible to JavaScript), `SameSite: Lax` (CSRF defense).
- **TLS Enforcement:** `Secure: true` flag automatically set when the gateway is accessed via HTTPS or forwarded TLS headers.
- **Admin Isolation:** Destructive operations (`/api/version/shutdown`, `/api/version/update`, `/admin/health/reset`) strictly require session cookies or local CLI tokens. **Standard client API keys are rejected outright.**

---

## 6. Debug Surface Clamping

- **Go Profiler (`pprof`):** Disabled by default (`PPROF_ENABLED=false`).
- **Authorization Gate:** When enabled for debugging, all `/debug/pprof/*` endpoints are explicitly gated behind `RequireAdminAuth()` middleware to prevent public memory dumping or goroutine profiling.
