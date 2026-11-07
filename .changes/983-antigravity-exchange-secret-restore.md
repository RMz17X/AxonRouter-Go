# 983 — Antigravity exchange: restore client_secret alongside PKCE (982's no-secret assumption falsified live)

## Symptom

Add-account sign-ins with the v1.9.23 flow failed at the final step:
the dashboard exchange returned 502 "token exchange returned status
400". The 982 change had been verified only against a local token stub,
which accepted the no-secret form. The real Google endpoint does not.

## Root cause

Fragment 982 assumed agy's client is a public installed-app client
whose exchange proves possession of the PKCE verifier INSTEAD of a
client secret. That assumption is wrong for this client: the live
token endpoint rejects a secret-less authorization_code exchange for
it even when the PKCE verifier matches. The same assumption was
falsified independently on the JS router and fixed there (v0.1.33) by
sending the embedded secret again; pre-982 Go always sent the secret
and its add-account flow worked.

## Fix (internal/handlers/oauth/antigravity.go)

- The exchange form once again sends `client_secret` — the value the
  provider config already carries (`getAntigravityOAuthConfig`), the
  public secret embedded in agy itself — TOGETHER WITH the PKCE
  `code_verifier` from 982. Nothing else in the flow changes:
  authorize (pinned Google redirect, per-request S256 challenge,
  server-side single-use verifier store), the {code, state}
  requirement, post-token behavior (972 identity pinning), and the
  refresh path are all exactly as 982 left them.
- The non-200 log line now records Google's error body. Error bodies
  carry only error/error_description — never tokens — and a bare
  status code made this failure undiagnosable from the server log.
  Success bodies are still never logged.

## Verification (live, decisive)

- 2026-11-07 13:30 WIB: a real authorization code from a real sign-in
  exchanged against the UNPATCHED v1.9.23 build → Google 400
  (reproduced a second time at 13:35 through an operator restart
  error; that run is not evidence about the patch).
- 2026-11-07 13:39 WIB: a fresh real code exchanged against the
  patched build → HTTP 200, connection created for the signed-in
  account. The exchange leg is proven end-to-end on the real
  endpoint.
- Mechanical test update (edited, not run — standing rule):
  antigravity_test.go's mock token endpoint now REQUIRES a non-empty
  client_secret (mirroring the live endpoint) instead of rejecting
  its presence.
- Not covered by this proof: generation with the new account's
  tokens from the build VM (its upstream DNS resolves to a reserved
  range and the SSRF guard blocks the dial before egress). The
  generation leg is verified on the hijilaptop staging instance,
  whose egress is normal.
