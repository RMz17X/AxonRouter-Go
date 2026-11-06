# 971 — Antigravity: agy teardown §7 completion (analysis record, no code changes)

Completes the remaining open items of the official `agy` v1.5.1 teardown
(`~/workspace/agy-analysis/AGY-IDENTITY-SPEC.md` §7, resolved in place
2026-11-06) and records what each verdict means for this router.
**This fragment intentionally ships no code changes**: the application
target (full-agy parity vs the pinned-per-account hybrid) is awaiting
confirmation, so the deltas below are recorded, not applied. The
fragment-970 dual-config refresher work is untouched.

## Verdicts

- **Endpoint rule — LIKELY `daily-cloudcode-pa.googleapis.com`.**
  `main.main` lazy-defaults a package-main endpoint string global to the
  daily literal at three sites (the only default-value use of either host
  literal in the binary); all prod-literal references are
  compare/conversion sites on the business/AICode surface. Not CERTAIN:
  the global cannot be proven to be the provider's endpoint field, and
  runtime proof is gated behind a completed Google sign-in.
- **Sign-in scope list — CERTAIN (runtime capture).** The preserved
  v1.5.1 binary's own printed authorization URL (acpauth consumerOAuth
  flow) uses client `1071006060591-…`, redirect
  `https://antigravity.google/oauth-callback`, PKCE S256,
  `access_type=offline`, `prompt=consent`, and scopes in order:
  cloud-platform, userinfo.email, userinfo.profile, cclog,
  experimentsandconfigs, aicode, `openid`. This corrects the teardown
  spec §4.1 grading of the 884354919052 client as "the CLI sign-in
  client": the interactive standalone sign-in does not use it. Whether
  the `RunLogin` configs carry the identical scope list is UNKNOWN.
- **Token file format — informational only.** agy persists via
  `cliFileTokenStorage` (CLI) / `fileTokenStorage` + composite (LS),
  keyring primary, with oauth2.Token JSON field names. The previously
  assumed fallback filename (`oauth_tokens.json`) is NOT corroborated
  by any literal in the binary (only `mcp_oauth_tokens.json` exists).
  No router surface is affected (tokens are stored in the DB).
- **Updater TTL — informational only.** 900 s (15 min), read from the
  freshness helper's immediate `0xd18c2e2800` ns compared against
  `time.Since(mtime)` of `last_check.timestamp`. No self-updater parity
  is needed in the router.

## Recorded deltas (NOT applied — pending application-target confirmation)

- **Scopes:** `internal/handlers/oauth/antigravity.go` currently requests
  cloud-platform, aicode, userinfo.email, userinfo.profile, cclog,
  experimentsandconfigs. Exact agy parity would add **`openid`** (and
  match agy's ordering, with aicode moved after
  experimentsandconfigs). One-scope delta; the set is otherwise equal.
- **Endpoints:** chat / usage / media already use the daily host
  (consistent with the LIKELY verdict — no change). Discovery / probe
  (loadCodeAssist, onboardUser, connection test) currently use the prod
  host; under a full-agy application they would move to daily. Note the
  verdict grade is LIKELY, not CERTAIN.
- **OAuth client:** unchanged by this fragment. The runtime capture
  strengthens the current arrangement: the router's primary client
  (1071006060591) is the one agy's interactive sign-in actually uses.
