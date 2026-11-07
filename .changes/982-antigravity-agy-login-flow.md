# 982 — Antigravity add-account login mirrors agy /login (PKCE + paste-code)

The Antigravity sign-in was the last surface still telling a different
story from agy: it rode the inherited dashboard-callback flow. It now
mirrors agy's /login on all three points where they differed. Scope is
Antigravity ONLY — every other provider's OAuth flow, the shared
/callback page, and the oauth-handoff machinery are untouched.

## What agy does (proven)

- Authorize URL: client_id 1071006060591-…,
  redirect_uri=https://antigravity.google/oauth-callback (a Google-hosted
  page that DISPLAYS the authorization code for manual copy-paste), PKCE
  S256, scopes in agy order, access_type=offline, prompt=consent, state.
  (Runtime capture, teardown §7; Ricki's own URL capture 2026-11-07.)
- Exchange: code + code_verifier + that same redirect_uri; NO
  client_secret — the client is a public installed-app client.

## What changed

### Authorize (internal/handlers/oauth/antigravity.go)

- redirect_uri is FIXED to the new constant `antigravityRedirectURI`
  (internal/handlers/oauth/antigravity_pkce.go). A client-supplied
  ?redirect_uri= is no longer honored for this provider; the old
  `getAntigravityRedirectURI` helper is deleted.
- Per request the handler mints a PKCE pair (the package's established
  `pkceVerifier()` / `sha256Base64()` primitives) and adds
  code_challenge + code_challenge_method=S256 to the URL. Client id,
  scope list/order, access_type, prompt and state semantics are
  unchanged; the ?redirect=true variant still works; the JSON response
  still returns the URL + state, with redirectUri now the fixed Google
  page.

### Verifier storage (internal/handlers/oauth/antigravity_pkce.go, new)

- Process-wide store keyed by state, TTL 10 minutes, single-use
  (consume deletes unconditionally, before the token call, so a replayed
  state can never ride a second attempt; expired entries report as
  missing).
- Why not the existing PKCE machinery: the generic handlers
  (pkce.go/cline.go) hand verifier custody to the CLIENT — their
  authorize JSON returns codeVerifier and the exchange reads it back
  from the request body. agy's flow never exposes the verifier to the
  client, and this provider's exchange is bespoke (it also owns
  projectId preservation and the fragment-972 identity pin), so it
  cannot ride completePKCEExchange/persistConnection. The store is the
  smallest equivalent inside the oauth package and deliberately follows
  the Codex session registry's shape (mutex-guarded map, TTL sweep on
  write, drop-on-read).

### Exchange (internal/handlers/oauth/antigravity.go)

- Accepts {code, state} (the pasted-code path via cleanAuthCode is
  unchanged, including full-URL pastes). Missing code → 400; missing
  state → 400; unknown/expired/reused state → 400.
- The token form is now {code, client_id, redirect_uri =
  antigravityRedirectURI (server-side constant), grant_type,
  code_verifier} — NO client_secret. A client-supplied redirectUri
  field is still parsed (older dashboard builds do not break) but never
  honored.
- Everything after a successful token response is functionally
  identical: connID derivation, the data map, projectId preservation,
  and the fragment-972 identity pin (existing pin carried verbatim,
  fresh snapshot for new/unpinned connections).
- The refresh path is NOT touched: fragment 970's dual-config refresher
  lives in internal/proxy/oauth and reads its own configs from
  internal/providers; KnownOAuthConfigs is unchanged.

### Axon-compat dispatch (internal/handlers/oauth/batch3_dynamic2.go)

- dynExchange now threads the client's state into the delegated
  Antigravity payload (the handler consumes the verifier by state) and
  Antigravity joins the gate's no-client-codeVerifier set, since its
  verifier is server-side now. The gate's redirectUri requirement for
  the compat route is unchanged.

### Frontend (web/src/api/client.ts, ProviderDetailView.svelte)

- client.getAntigravityAuthorizeUrl() takes no redirectUri anymore;
  client.antigravityExchange(code, state) posts only {code, state}.
- The modal is the paste-code pattern with the layout Ricki confirmed
  (2026-11-07): the login step shows (1) the login URL itself in a
  readable field, (2) an explicit "Open login page" button that opens
  the new tab ONLY on a manual click, and (3) a Copy button for the
  URL — plus the paste-code field and Connect. Pressing Add / Log in
  must NOT auto-open a tab: the previous window.open in
  openAntigravityOAuth is removed (other providers keep their
  auto-open; this requirement is Antigravity-only). The banner,
  divider, step-2 label, placeholder and helper text instruct the user
  that Google's Antigravity page will display a code to copy and paste
  here, then Connect.
- The dashboard /callback auto-handoff no longer applies to
  Antigravity: consumeOAuthCallback returns early for the provider, and
  the antigravity-specific postMessage/BroadcastChannel completion
  branches are removed (the generic handoff keeps working for every
  other provider). The now-dead antigravityCallback() helper and the
  oauthLoopbackCallbackURL import are removed.

## Before / after — authorize URL parameter set

- Before: client_id, redirect_uri=<dashboard origin>/callback (or
  client-supplied), response_type, scope, access_type, prompt, state.
- After: client_id, redirect_uri=https://antigravity.google/oauth-callback,
  response_type, scope (same order), access_type, prompt, state,
  code_challenge, code_challenge_method=S256.

## Mechanical test updates

- internal/handlers/oauth/antigravity_test.go: authorize assertions now
  pin the fixed Google redirect (the request still passes a legacy
  ?redirect_uri= to prove it is ignored), the full 7-scope agy list
  (the old expectation predated the aicode/openid alignment), prompt
  and PKCE parameters; TestGetAntigravityRedirectURI deleted with the
  helper; exchange error cases replace the redirect-uri cases with
  missing/unknown state; the success test authorizes first and its mock
  token endpoint now requires the fixed redirect, a verifier matching
  the authorize challenge, and no client_secret — and the re-exchange
  leg mints a fresh authorize round because verifiers are single-use.
  (File sits behind the sqlite_legacy tag; its package test build is
  broken at HEAD by freebuff_test.go's db.OpenDatabase reference —
  pre-existing, identical before and after this fragment.)
- web/src/api/client.test.ts: the Antigravity contract test now expects
  GET /api/oauth/antigravity/authorize with no query and an exchange
  body of exactly {code, state}.

## Deliberately unchanged

- getAntigravityOAuthConfig still surfaces the configured client secret
  to the package; the sign-in exchange simply no longer sends it.
  Refresh/exchange code paths outside this handler (fragment 970) keep
  their own behavior.
- The risk-notice gate before the Antigravity modal, the popup window
  itself, and cleanAuthCode's paste tolerance.
