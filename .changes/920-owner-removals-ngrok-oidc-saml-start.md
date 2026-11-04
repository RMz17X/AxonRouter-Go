# Owner-ordered removals — ngrok tunnel, OIDC dashboard login, SAML start

Three features deleted at the owner's order (2026-11-04), reversing parts of
batches 4 and 7 plus the upstream OIDC surface. Removal-only change; no new
behaviour. Diff: 14 files, 58 insertions, 978 deletions. Not committed by
the implementer — parent verifies and commits.

## 1. Ngrok tunnel (batch-4 port) — removed

- Deleted `internal/handlers/dashboard/batch4_tunnel.go` (275 lines: the
  `ngrokState` manager driving the operator's ngrok agent CLI, the status
  shape with its honest `installed:false, phase:"not_installed"` surface,
  `HandleStartNgrokTunnel`, `HandleStopNgrokTunnel`).
- Removed the router registrations `POST /api/tunnel/ngrok/start` and
  `POST /api/tunnel/ngrok/stop` (router.go).
- Kept: the separate Cloudflare/Tailscale tunnel family
  (`/api/tunnel/status|enable|disable`, `/api/tunnel/tailscale-*`) — a
  different feature, not part of the order. Batch 4's other routes
  (proxy-groups, proxy-pools, circuit-breakers, routing/latency) untouched.

## 2. OIDC — dashboard SSO login feature — removed entirely

- Routes: `POST /api/auth/oidc/test` and `GET /api/auth/oidc/start`
  registrations removed from router.go.
- `internal/handlers/sso/batch7_sso_start.go` deleted whole (see item 3 —
  its remaining handler was the SAML start).
- `internal/handlers/sso/sso.go`: removed `HandleOidcTest` and its
  OIDC-exclusive helpers `fetchDiscovery`, `probeClientSecret`,
  `discoveryDocument`, `probeOutcome`, the `clientInvalidRe`/`grantCodeRe`
  regexes, `mustRead` (only the probe read a response body), and the
  constants `discoveryPath`, `oidcCallback`, `defaultScopes`, `testCode`,
  `testCodeVerifier`. Package doc updated. Shared helpers the SAML handlers
  use — `decodeJSONBody`, `publicOrigin`, `firstNonEmpty`, `writeError`,
  the certificate helpers — all stay.
- `internal/auth/session.go`: removed the `Oidc`, `OidcName`, `OidcEmail`
  JWT claim fields and their copying in `SignWith`; comments updated.
  Existing tokens simply lose those fields on next parse (JSON unknown
  fields are ignored on verify, and the fields were `omitempty`).
- `internal/handlers/dashboard/auth.go` (`HandleAuthStatus`): removed the
  `claims.Oidc` login-method case and the response keys `oidcConfigured`,
  `oidcLoginLabel`, `oidcName`, `oidcEmail`, `oidcLogin`. The `ssoType`
  fallback changed from `"oidc"` to `"saml"` (a stored `ssoType` value
  still passes through verbatim). SAML keys (`samlConfigured`,
  `samlLoginLabel`, `samlName`, `samlEmail`, `samlLogin`) byte-identical.
- `internal/handlers/dashboard/settings.go`: `secretSettingKeys` is now
  `[]string{"password"}` (`oidcClientSecret` removed, comment updated); the
  `importDatabase` comment that cited losing OIDC on a settings wipe was
  trimmed to match. Consequence, recorded deliberately: a legacy stored
  `oidcClientSecret` value is no longer treated as a secret — it is
  ordinary inert settings data and would ride along in GET /api/settings
  responses and backups like any other leftover key.
- Web:
  - `web/src/api/client.ts`: removed `oidcIssuerUrl`, `oidcClientId`,
    `oidcScopes`, `oidcLoginLabel` from the settings interface.
  - `web/src/components/LoginView.svelte`: removed the OIDC state, the
    status-response reads of the oidc keys, `handleOidcLogin`, the OIDC
    button, the OIDC header line, and the OIDC branches of the two SSO
    notice lines (now SAML-only text).
  - `web/src/components/ProfileSettingsView.svelte`: removed the OIDC
    state fields, both settings-load blocks' oidc reads, the four oidc
    keys from the save payload, the `OpenID Connect (OIDC)` auth-mode
    option, and the whole `authMode === 'oidc'` form block (issuer,
    client ID, scopes). `authMode` type is now `'password' | 'saml'`.
  - `web/src/lib/commandPalette.ts`: palette label `SSO / OIDC / SAML`
    → `SSO / SAML` (cosmetic, same section).

## 3. SAML start (batch-7 port only) — removed

- Removed the `GET /api/auth/saml/start` registration and
  `HandleSamlStart` with its exclusive helpers (`buildSAMLAuthorizeURL`,
  `samlBaseURL`, and the shared-with-OIDC flow helpers
  `buildAuthorizationURL`, `setSSOFlowCookie`, `shouldUseSecureCookie`,
  `redirectLoginError`, `randomBase64URL`, `randomHex`, `formEncode`,
  `encodeURIComponent`, `oidcRuntimeConfig`). None of them were used by
  `sso.go` or its tests, so `batch7_sso_start.go` was deleted whole and
  the `ssoStartH` construction left router.go with it; the batch-7
  comment block above the session/reset/internal-proxy routes was
  reworded to drop the SSO-initiator mention.

## Kept — deliberately untouched

- SAML test + metadata handlers (`HandleSamlTest`, `HandleSamlMetadata`,
  `spMetadataXML`, cert helpers), SAML session claims, SAML config
  exposure in `HandleAuthStatus`, and the SAML settings form — upstream
  feature, not ordered removed.
- Gateway-native auth (login/logout/status/session/reset-password) and
  the password flow.
- All provider-side OAuth (`internal/handlers/oauth/*`,
  `internal/proxy/oauth/*`, provider executors) — the owner confirmed
  provider OAuth is mature and off-limits; the AWS SSO OIDC endpoints in
  the Kiro flow are provider machinery, not the dashboard feature.
- Batch 7's other routes (mcp audit+transports, credentials, system,
  console-logs, internal proxy).

## Judgment calls

- **`ssoPasswordDisabled` (dashboard/auth.go)**: beyond the brief's named
  keys, the OIDC arm of the SSO-only password gate was also removed —
  with no OIDC sign-in flow left, a legacy `authMode:"oidc"` + stored
  OIDC config would otherwise lock the operator out of password login
  pointing at a deleted feature. The SAML arm is outcome-identical
  (same condition, same message, same 403): the mode switch still lists
  `"oidc"` so legacy values parse the same, and `ssoTypeOf` now resolves
  to `"saml"` only when the setting or the legacy saml authMode says so,
  else `""` (previously `"oidc"`) — neither value equals `"saml"`, so the
  gate's SAML/password outcomes are unchanged. The now-dead
  `oidcConfigured` and `trimTrailingSlashes` helpers were deleted with
  their last call sites.
- **LoginView deriveds**: `activeSsoType` was dropped rather than pinned
  to SAML; `samlAvailable` is now `isSsoEnabled && samlConfigured`, and
  `'oidc'` left the `isSsoEnabled` authMode list — a legacy
  `authMode:"oidc"` install now gets the plain password form instead of
  an SSO notice for a flow that no longer exists.
- **Asymmetry left on purpose (owner follow-up)**: the LoginView SAML
  button remains (brief: keep the SAML UI intact) and still navigates
  to `/api/auth/saml/start`, which item 3 removes — the button can only
  appear when SAML is configured, and now lands on a 404. Removing the
  button or restoring a start route is a product decision, not part of
  this order.
- **Test files** (updated to the new behaviour, not run — direct checks
  only per the standing rule): `sso_test.go` lost the oidc/test mount
  and `TestHandleOidcTest_RequiresIssuer` (required for `go vet` to
  compile). `auth_test.go`: the SSO-only gate scenario now seeds SAML
  (the surviving flavor) instead of OIDC, and the status test asserts
  only the SAML flag/identity. `settings_test.go`: the export/import
  secret tests now exercise `password` as the sole secret key; the
  `oidcClientSecret` fixture and its assertions were removed with the
  key's secret status.

## Verification (direct checks, verbatim)

- `~/bin/heavy ~/go-sdk/bin/go build ./...` and `go vet ./...`: both
  exit 0, no output (vet re-run after the test edits, exit 0).
- Dangling-reference grep over `internal/` + `cmd/` + `web/src/`:
  `ngrok`/`Ngrok` — zero hits in Go; `oidc` in production Go only in
  dashboard/auth.go's legacy `"oidc"` authMode case + explanatory
  comments and in provider-side `internal/proxy/oauth/kiro.go`
  (untouched, off-limits); `oidc` in `web/src/` — zero hits.
- svelte-check (`./node_modules/.bin/svelte-check --tsconfig
  ./tsconfig.app.json` in web/): "svelte-check found 77 errors and 57
  warnings in 30 files" — exactly the 77/57 baseline, zero added.
- Live smoke (scratch binary + DATA_DIR under `~/tmp-smoke/removals920/`,
  port 21320, stdin from a regular file; server killed by exact PID
  afterwards). Control `GET /api/auth/oidc/callback` (never existed at
  this HEAD) → 404, and:
  - `GET /api/auth/oidc/start` → 404; `POST /api/auth/oidc/test` → 404;
    `GET /api/auth/saml/start` → 404; `POST /api/tunnel/ngrok/start` →
    404; `POST /api/tunnel/ngrok/stop` → 404.
  - Password login end-to-end: `POST /api/auth/login {"password":"123456"}`
    → 200 `{"mustChangePassword":false,"success":true}` + cookie;
    `GET /api/auth/session` → `"authenticated":true`, `"reason":"valid_paseto"`,
    token payload `{"authenticated":true,"exp":…,"iat":…,"saml":false}`
    (no oidc fields); `POST /api/auth/logout` → 200 `{"success":true}`;
    session probe after logout → `"authenticated":false`,
    `"reason":"missing_cookie"`.
  - Kept SAML surface with the session: `GET /api/auth/saml/metadata` →
    200 `application/xml`; `POST /api/auth/saml/test {}` → 400
    `{"error":"Single Sign-On Service URL (samlEntryPoint) is required"}`.
  - `GET /api/auth/status` (session) →
    `{"authMode":"password","authenticated":true,"displayName":"Password user","hasPassword":false,"loginMethod":"Password","requireLogin":true,"samlConfigured":false,"samlEmail":null,"samlLogin":false,"samlLoginLabel":"Sign in with SAML SSO","samlName":null,"ssoType":"saml"}`
    — no oidc keys, SAML keys intact.
  - `GET /api/settings` (session) → 200.
- Independent re-verification (fresh binary, `~/tmp-smoke/removals920b/`,
  port 21321, server killed by exact PID): build + vet exit 0 again,
  svelte-check again exactly 77 errors / 57 warnings, all five removed
  routes again 404 (control route also 404), and the login → session →
  status → SAML metadata/test → settings → logout sequence reproduced
  with identical shapes (status body byte-identical apart from the port;
  session payload carries no oidc fields).
