# Backend parity batch 7 — auth, MCP transports, credentials, system, console-logs, internal proxy

All 17 in-scope routes are now served. No route was blocked; two
behaviours were consciously not ported (see Deviations). Axon contract
sources: `src/app/api/{auth,credentials,mcp,system,internal}/**/route.(js|ts)`
(route.js wins), `src/lib/auth/{oidc,saml}.js`,
`src/lib/{internalProxyTokens,usageProxyOutcome,usageStatus,usageStatusPatches,usageStatusSnapshots,connectionStatus}.ts`,
`src/lib/credentials/{normalizer,validator,matcher,importer,codexAccount}.ts`,
`open-sse/mcp-server/{server,scopes,audit,auditRuntime}.ts` +
`open-sse/mcp-server/schemas/tools.ts`, `src/sse/services/apiKeyAuth`,
`requireManagementAuth.ts` + `ipValidator.ts`, `src/dashboardGuard.js`.

Premise corrections against the batch brief (verified in the Go tree):
the brief assumed Go already had `/api/auth/oidc/callback`,
`/api/auth/saml/acs`, `/api/mcp/status`, `/api/mcp/tools`,
`/api/mcp/invoke`. None of those exist in Go. The sso package had only
the test/metadata handlers; batch 6's protocol handlers self-call the
MCP tool layer in-process and reproduce the degraded-backend behaviour.
So the SSO `/start` routes point their callback/ACS URLs at the paths
Axon uses (`/api/auth/oidc/callback`, `/api/auth/saml/acs`) and the MCP
core (registry, scopes, invoke) remains batch 8's work — this batch
ports the four MCP routes that exist independently of it.

## Per-route results

### AUTH
- `GET /api/auth/oidc/start` — ported (`internal/handlers/sso/batch7_sso_start.go`).
  Runtime config gate (authMode ∈ {oidc, both} + issuer/clientId/secret)
  → 307 `/login?error=oidc_not_configured`; discovery fetch; state,
  nonce, PKCE S256; authorize URL params in upstream insertion order;
  `oidc_state`/`oidc_nonce`/`oidc_code_verifier` cookies (HttpOnly,
  SameSite=lax, Path=/, Max-Age=600, secure per AUTH_COOKIE_SECURE /
  x-forwarded-proto); any failure → 307 `/login?error=<urlencoded msg>`.
- `GET /api/auth/saml/start` — ported (same file). node-saml AuthnRequest
  (raw-deflate + base64 + URL-encoding), unsigned (no SP key upstream),
  `saml_state` cookie = request ID; unconfigured →
  `/login?error=saml_not_configured`.
- `GET /api/auth/session` — ported (`internal/handlers/batch7_auth.go`).
  Always 200 `{authenticated, reason, payload?, host, forwardedProto,
  secureRequest}`; reasons missing_cookie / valid_paseto /
  expired_token / invalid_signature / invalid_token.
- `POST /api/auth/reset-password` — ported (same file). Ignores the body,
  sets the stored dashboard password to null, returns `{success:true}`.
  Gate is upstream's LOCAL_ONLY rule (see auth-surface note).

### MCP
- New store: `mcp_tool_audit` table in both schema dialects
  (`internal/db/schema.go`, `schema_pg_defs.go`) + repo
  (`internal/db/batch7_mcp_audit.go`) porting audit.ts: input SHA-256 of
  the JSON input, output summary = string or `JSON.stringify(output ||
  {})` truncated to 200 (197 + "..."), ISO-millis created_at.
- `GET /api/mcp/audit`, `GET /api/mcp/audit/stats` — ported
  (`internal/handlers/batch7_mcp_audit.go`), behind the
  requireManagementAuth port. List returns raw rows + total/limit/offset;
  stats are the last-24h aggregates with top-5 tools.
- `GET|POST /api/mcp/sse`, `POST /api/mcp/stream` — ported
  (`internal/handlers/batch7_mcp_transport.go`). Client API-key gate
  (open when no keys configured; else Bearer-only, 401 JSON-RPC -32000
  "Missing API key" / "Invalid API key"). Full 25-tool registry from
  schemas/tools.ts with phase/auditLevel/requiredScopes stripped from
  tools/list. initialize → protocolVersion 2024-11-05, serverInfo
  axonrouter/0.5.4; ping → {}; unknown method → 404 -32601 with the raw
  method interpolated ("undefined" when absent); SSE POST body/method
  errors → 400 -32600 with id forced to null; stream parse failure →
  unknown-method 404 with the id key absent (JSON.stringify drops
  undefined ids — echoed exactly). SSE GET sends the endpoint event,
  30s `: ping` keepalives, 600s lifetime.
  Faithful dead-end, verified in upstream source: the HTTP transports
  hardcode a null API-key record, every tool requires ≥1 scope, so every
  `tools/call` over HTTP ends in `Missing MCP scopes: ...`
  (code scope_denied) and is audit-logged; unknown tools return
  "Unsupported tool" without an audit row. The 25 tool handlers
  themselves are reachable only via /api/mcp/invoke and stdio upstream —
  batch 8 territory, not stubbed here.

### CREDENTIALS (`internal/handlers/dashboard/batch7_credentials*.go`)
- `GET /api/credentials/export` — connections carrying credential
  material, reduced to upstream's 33-key whitelist, envelope
  `{format:"universal-credentials", exportedAt, entries}`.
- `POST /api/credentials/import` — full pipeline port
  (extract → normalize → sanitize → duplicate check → match →
  preserve/update/create). Error mapping: INVALID_JSON,
  INVALID_IMPORT_PAYLOAD, DUPLICATE_IMPORT_RECORDS → 400 with
  errorCode; INVALID_LEGACY_STATUS_FIELDS and
  REPLACE_MODE_VALIDATION_FAILED are mapped like upstream's route but
  are dead branches (nothing in current Axon throws them). A matched
  connection is probed without persisting; still-valid → preserved,
  invalid → updated. Imports never delete.

### SYSTEM (`internal/handlers/batch7_system.go`)
- `GET /api/system/machine-id` — `{machineId}` + Cache-Control:
  no-store. sha256(persisted raw id + salt + cli-secret extra only for
  the cli-auth salt)[:16], persisted raw id under the data dir.
- `GET /api/system/memory` — `{dataDir, dbEngine, dbSize,
  dataDirSizeMB, inMemory:{fitnessPools, geoPools, freebuff}}`; pool
  counts read from the live pool state (`poolstate.GeoPoolCount`,
  executor freebuff session size — new small accessors, batch7 files).
- `GET /api/system/version` — literal port of route.js incl. its source:
  Docker Hub tags of `rickicode/axonrouter`, semver-filtered, 12h
  in-memory cache of successful results, `{currentVersion,
  latestVersion, hasUpdate, releaseUrl}`; lookup failure →
  `{currentVersion, hasUpdate:false}` (+ error field on exceptions).

### CONSOLE-LOGS
- `GET|DELETE /api/console-logs`, `GET /api/console-logs/stream` —
  upstream's top-level routes are the canonical implementation and its
  translator routes re-export them; Go already had the feature at
  `/api/translator/console-logs*`. Smallest faithful change: the three
  top-level aliases were added to the same RequireConsoleLogAuth group
  in SetupConsoleLogRoutes (`internal/handlers/router.go`). No
  duplicated log machinery.

### INTERNAL (`internal/handlers/batch7_internal_*.go`)
- `POST /api/internal/proxy/resolve` — ported: route-contract allowlist
  (openai/anthropic path sets), alias-resolved provider, active
  connections filtered by the connectionStatus port, provider strategy
  resolution, round-robin pick with the consecutive-use < 3 rule,
  sanitized chosen connection + fallback chain (key omitted when
  empty), ttlSeconds from GO_PROXY_RESOLVE_CACHE_TTL_SECONDS clamped
  [5,10] (default 7). Errors: 400 invalid_request / invalid_route_contract,
  503 no_routable_connection with `owner:"axonrouter"`.
- `POST /api/internal/proxy/report` — ported: usage row + request
  detail recorded, then the outcome folds into the connection status
  through the usageStatus port (syncUsageStatus with stale guard,
  legacy-field stripping, 7-day retry caps, auth-recovery preservation;
  quota interpreters for codex / kiro / amazon-q / antigravity;
  auth-blocked, transient-timeout, processing-error and codex
  live-quota branches in upstream order). Errors: 400 invalid_request,
  500 report_ingestion_failed + message.

## Auth-surface note — internal endpoints (read before exposing)

- Axon requires the header `x-internal-auth` to equal a per-purpose
  token (`internalProxyResolveToken` / `internalProxyReportToken`)
  stored in settings and auto-generated (64 hex chars) on first read —
  so the tokens effectively always exist. Failure → 401
  `{ok:false, error:"unauthorized"}`.
- Upstream these routes are additionally behind dashboardGuard (not in
  PUBLIC_API_PATHS), but the operative credential is the internal
  token: the intended caller is the separate WorkerProxy process, which
  holds only these tokens and no dashboard session.
- The Go mount mirrors that operative model: the routes sit in the
  public area of SetupServerRouter with ONLY the handler-level token
  check — no session, no client API key. Consequence: anyone who learns
  a token can resolve routing and report outcomes (which can disable or
  degrade connections via the status machinery). The tokens live in the
  settings row, i.e. they are readable through dashboard settings
  surfaces by anyone with dashboard access. Upstream has a
  regenerateInternalProxyTokens helper but no route for it in this
  batch's scope; rotation here means editing settings.
- reset-password's gate, for contrast: valid CLI token, OR a direct
  private-network request from an authenticated caller (requireLogin
  off counts as authenticated). Denial → 403
  `{error:"Local only: CLI token required"}`.

## Smoke evidence (scratch server, throwaway data only)

Scratch binary on :20137, DATA_DIR under ~/tmp-smoke, requireLogin
flipped off in the scratch DB. Verified live:
- session: no cookie → missing_cookie; minted valid token →
  authenticated + payload + valid_paseto; tampered signature →
  invalid_signature; garbage → invalid_token.
- reset-password: with a seeded throwaway bcrypt password, POST →
  {success:true}; afterwards /api/auth/status hasPassword=false and the
  settings password is JSON null.
- OIDC start (mock discovery on localhost): 307 to the mock's
  authorization_endpoint with response_type/client_id/redirect_uri
  (.../api/auth/oidc/callback)/scope=openid+profile+email/state/nonce/
  code_challenge/S256 in order + the 3 cookies with the right
  attributes. Dead issuer → 307 /login?error=Failed%20to%20load%20OIDC
  %20discovery%20document%20from%20...; authMode=password →
  oidc_not_configured. This verifies redirect construction against
  Axon's source logic; no real IdP exists in this environment.
- SAML start (seeded entry point + cert): 307 to the entry point;
  SAMLRequest raw-deflate-decodes to an AuthnRequest whose ID equals
  the saml_state cookie, Destination = entry point,
  AssertionConsumerServiceURL = {origin}/api/auth/saml/acs, Issuer =
  urn:9router:sp (deviation below). Cleared entry point →
  saml_not_configured.
- MCP: audit list/stats empty → zero shapes; tools/call
  axonrouter_get_health → isError "Missing MCP scopes: read:health" and
  a scope_denied audit row with output_summary "{}"; unknown tool →
  "Unsupported tool", no row; initialize/ping/unknown-method/garbage
  bodies match the contract incl. id echo rules; tools/list = 25 tools,
  stripped fields; SSE GET → endpoint event + X-MCP-Session-Id; with an
  API key configured: no key → 401 -32000 Missing API key, wrong key →
  Invalid API key, valid key → 200.
- credentials: export of seeded throwaway connections (format +
  whitelist); import created/updated paths (OAuth record matched by
  name and updated after a failed validity probe); round-trip re-import
  of an export → updated, connection count unchanged; duplicate payload
  → 400 DUPLICATE_IMPORT_RECORDS with upstream's message; malformed
  JSON → INVALID_JSON; array-less payload → INVALID_IMPORT_PAYLOAD;
  missing provider → skipped INVALID_RECORD with index; apikey without
  name → skipped INVALID_RECORD without index. "preserved" needs a
  probe that passes against a real provider, unreachable with
  throwaway credentials — the preserve branch is a straight port,
  exercised only in its invalid → update direction.
- system: machineId 16 hex, stable across restarts; memory returns the
  documented shape with real pool counters at 0; version returns
  currentVersion "1.9.16-exp.1" (registry reachable from the VM; no
  semver tag outranks it in the ported comparison — see deviation).
- console-logs aliases: GET returns the log buffer, DELETE
  {success:true}, stream serves SSE with upstream's headers.
- internal: no/wrong token → 401 {ok:false,error:"unauthorized"};
  tokens auto-generated into settings on first call; resolve with a
  seeded eligible connection → 200 resolution (statusSource quotaState,
  ttlSeconds 7, fallbackChain omitted for a single connection);
  ineligible-only pool → 503 no_routable_connection; bad path → 400
  invalid_route_contract; report (ok + tokens) → usage + detail rows
  written; report 401 auth error → connection disabled with
  reasonCode auth_invalid; generic upstream error → healthStatus
  degraded; malformed body → 400 invalid_request.
- Build: `go build ./...` green via ~/bin/heavy; `go vet` clean on all
  touched packages; gofmt clean on all batch-7 files. No unit suites
  run (owner rule).

## Deviations

- SAML default SP issuer is `urn:9router:sp`, not upstream's
  `urn:axonrouter:sp`: the existing Go SAML metadata/test handlers
  already use the 9router issuer, and metadata + AuthnRequest must
  agree. A settings-provided samlIssuer wins, as upstream.
- Session tokens are Go's HS256 JWTs, not PASETO; the reason string
  `valid_paseto` is upstream's literal and is returned verbatim.
  secureRequest is derived from the actual TLS state (the Go server
  terminates the connection itself).
- reset-password (and requireManagementAuth's local check) treat the
  socket peer as the client IP where upstream reads proxy-stamped
  headers (x-axonrouter-real-ip behind its peer-token check): the Go
  server terminates connections directly, so the socket peer is the
  same evidence. The trusted-peer header path is still honoured;
  upstream's NODE_ENV=development branch has no Go analogue and is
  dropped.
- requireManagementAuth's auditLogEnabled side effect (writing a
  management_auth_attempt audit entry) is not ported: Go has no
  management audit store. The gate itself (local whitelist or session)
  is exact.
- MCP audit list limit/offset: non-numeric input falls back to the
  defaults (50/0); upstream's Number(...) yields NaN and an unspecified
  SQLite result.
- system/memory reports the engine that is actually running
  (dbEngine "SQLite" + real file size here; "PostgreSQL" +
  pg_size_pretty on PG) where upstream hardcodes PostgreSQL. freebuff
  inflight/modelLocks/poolLimits/offerCaches report 0: Go keeps that
  state in the lease store and mutexes, not in-process maps; sessions
  is the real cache size.
- system/version is a literal port including its Docker Hub source and
  product name in releaseUrl; the Go build's own version string
  ("1.9.16-exp.1") is not a published tag there, so hasUpdate stays
  false until the Go image publishes semver tags to that repository.
- Internal resolve eligibility is computed from the single Go
  connection record (upstream's fallback path semantics); upstream's
  hot-state store can disagree with the stored entity in edge cases, so
  a connection whose hot-state record is missing/stale upstream could
  resolve differently there.
- Report ingestion: Go's usage/detail writers stamp arrival time (the
  reported observedAt is preserved inside the detail payload), the
  usage row's status carries upstream's "ok"/"error" values, and cost
  is recorded as 0 (upstream prices downstream of the report).
- Credentials: lastUsedAt/consecutiveUseCount are column-owned in Go —
  exports read them from the columns; imports never write them into
  the data blob.
- Quota interpreters iterate quota maps in sorted key order (Go maps
  are unordered); upstream uses payload insertion order. Only
  first-match picks among several simultaneously exhausted quotas can
  differ.
