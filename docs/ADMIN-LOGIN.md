# AxonRouter-Go: Admin & Dashboard Authentication

**Target Binary:** `axonrouter-go` (`v1.9.59`)  
**Package Authority:** [`internal/handlers/dashboard/auth.go`](file:///workspaces/AxonRouter-Go/internal/handlers/dashboard/auth.go), [`internal/auth/session.go`](file:///workspaces/AxonRouter-Go/internal/auth/session.go), [`internal/handlers/router.go`](file:///workspaces/AxonRouter-Go/internal/handlers/router.go)

---

## 1. Authentication Tiers & Boundary

AxonRouter-Go enforces 5 distinct authentication tiers across its HTTP surface:

| Tier | Protected Routes | Credential Mechanism | Header / Cookie |
|---|---|---|---|
| **Public** | `/health`, `/version`, `/api/version`, `/api/hello`, `/callback`, dashboard static assets | None | None |
| **Client API Key** | `/v1/*`, `/messages`, `/responses`, `/api/chat` | Argon2id verifier hashed with SHA-256 lookup index | `Authorization: Bearer <key>` or `X-API-Key: <key>` |
| **Dashboard Session** | `/api/*` (management routes, connections, usage, keys, models) | HMAC-signed session cookie or local CLI token | Cookie `axonrouter_session` or `Authorization: Bearer <cli-token>` |
| **Console Log Session** | `/api/translator/console-logs*` | Dashboard session or CLI token (client API keys strictly rejected) | Cookie `axonrouter_session` |
| **Admin Operations** | `/admin/health/reset`, `/api/version/update`, `/api/version/shutdown`, `/debug/pprof/*` | RequireAdminAuth middleware (session cookie or CLI token only) | Cookie `axonrouter_session` |

---

## 2. Admin Login Flow (`POST /api/auth/login`)

```mermaid
sequenceDiagram
  autonumber
  actor Operator as Admin / Browser
  participant Router as Chi Router (/api/auth/login)
  participant Auth as internal/handlers/dashboard/auth.go
  participant Limiter as internal/auth/session.go (Brute Force)
  participant DB as Repo (Settings / Credentials)

  Operator->>Router: POST /api/auth/login { "password": "...", "newPassword": "..." }
  Router->>Auth: HandleAuthLogin(w, r)
  Auth->>Limiter: LoginLocked(clientIP)
  alt IP is locked out
    Limiter-->>Auth: locked = true, retryAfter
    Auth-->>Operator: 429 Too Many Requests (Retry-After header)
  end
  Auth->>Auth: TunnelLoginBlocked(r, settings) & ssoPasswordDisabled(settings)
  Auth->>DB: verifyDashboardPassword(password)
  alt Password Invalid
    Auth->>Limiter: RecordLoginFail(clientIP)
    Auth-->>Operator: 401 Unauthorized (remainingBeforeLock: N)
  else Password Valid
    Auth->>Limiter: RecordLoginSuccess(clientIP)
    alt Remote host && Default password ("123456")
      Auth-->>Operator: 403 Forbidden ("mustChangeDefaultPassword")
    else Valid Password Change or Non-default
      Auth->>Auth: auth.Sign(auth.Secret(), now)
      Auth-->>Operator: 200 OK + Set-Cookie: axonrouter_session=...; HttpOnly; SameSite=Lax
    end
  end
```

---

## 3. Remote Host Protection (CVE-2026-56679 Mitigation)

1. **Default Password:** Initial root password defaults to `123456` if not overridden.
2. **Localhost Exception:** On loopback (`127.0.0.1`, `::1`), logging in with `123456` is permitted to allow initial configuration via Settings → Profile.
3. **Remote Caller Gate:** When accessed over non-loopback IP, AxonRouter-Go detects if the default password is still active:
   - Returns `403 Forbidden` with `"mustChangePassword": true`.
   - Rejects issuing session cookie until changed locally or initialized via environment variable `INITIAL_PASSWORD`.
   - Prevents public takeover of newly exposed cloud instances.

---

## 4. Progressive Brute-Force Rate Limiting

Login brute-force protection is enforced in-memory via [`internal/auth/session.go`](file:///workspaces/AxonRouter-Go/internal/auth/session.go):
- **Maximum Failed Attempts:** 5 attempts before lockout.
- **Lockout Duration:** Progressive sliding backoff (15s → 60s → 300s).
- **Client IP Resolution:** Evaluated via `auth.LoginClientIP(r)`.
  - When `TRUST_PROXY=true` or `TRUSTED_PROXIES` is set, reads `X-Forwarded-For` or `X-Real-IP`.
  - When untrusted, buckets strictly by TCP socket remote address.
- **Manual Reset:** Operators can reset the lockout table programmatically via `auth.ResetLoginLimiter()`.

---

## 5. Session Cookie Lifecycle

- **Cookie Name:** `axonrouter_session`
- **Payload:** Base64URL encoded JSON signature payload: `token = sign(auth.Secret(), timestamp)`.
- **Secret Authority:** Read from `SESSION_SECRET` environment variable or generated deterministically from DB seed.
- **Attributes:**
  - `HttpOnly: true` (prevents XSS access)
  - `SameSite: Lax`
  - `Path: /`
  - `Secure: true` (automatically enabled when request TLS is detected)
  - `Max-Age: 604800` (7 days)

---

## 6. Password Reset Procedures

### Method A: CLI Token / Local Reset
Run the binary with the reset command on the host:
```bash
axonrouter-go reset-password
# or via CLI menu:
axonrouter-go -> Settings -> Reset Password to Default
```

### Method B: Environment Variable Override
Set before starting the container or binary:
```bash
export INITIAL_PASSWORD="your-strong-production-password"
```

### Method C: Local Reset Endpoint
`POST /api/auth/reset-password` is exposed only to loopback requests or internal socket calls.
