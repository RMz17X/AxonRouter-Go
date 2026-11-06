# 972 — Antigravity: identity pinned per account (hybrid) + agy scope parity

Ricki's decision (2026-11-06): the router keeps the fragment-969 agy CLI
identity SHAPE, but the identity is PINNED PER ACCOUNT at OAuth time and
never changes for the life of the connection — the JS model's
one-account-one-fixed-identity principle, applied to the CLI identity
instead of the IDE profile. 969's pure runtime derivation (every account
presenting the serving host's live GOOS/GOARCH) is superseded: permanence
per account is the load-bearing property. This fragment also applies the
one CERTAIN delta from fragment 971 (the `openid` scope).

## The pin

- **Storage**: ONE JSON value in the connection's
  `providerSpecificData` under the new key **`antigravityIdentity`**.
  The legacy `clientProfile` value is NOT reused (its ide/cli enum is
  retired); it stays inert as before.
- **Snapshot shape** (`providers.AntigravityIdentity`):
  `{"userAgent": string, "platform": int, "ideVersion": string}` —
  everything that varies by runtime: the full UA string, the
  `loadCodeAssist` platform enum, and the CLI version the UA was built
  from. `LoadCodeAssistMetadata()` derives the numeric metadata
  (`ideType` 9, `pluginType` 2) from the pinned platform.
- **Parsing**: a pin counts only when it parses with a non-empty
  `userAgent` and a non-zero `platform`; anything else reads as
  unpinned.

## Write rules

- **OAuth creation** (`handlers/oauth/antigravity.go`, both the INSERT
  and the re-exchange UPDATE branch): a fresh snapshot of the current
  runtime derivation is stored with the new connection data. On
  re-exchange, an existing VALID pin in the stored data is carried into
  the rewritten data verbatim — re-login never re-pins. Exactly once
  per account lifetime; no other code path mutates an existing pin, and
  there is no UI for it.
- **Lazy backfill**: a connection without a pin is pinned on the first
  identity resolution that has store access:
  - chat: when the handler resolves the connection's data
    (`antigravityConnData`, antigravity connections only);
  - dashboard: `resolveAntigravityIdentity` (usage, models fetch,
    probe).
  The write is `db.Repo.SetConnectionAntigravityIdentity`: a single
  UPDATE whose WHERE clause requires the pin to still be absent
  (first writer wins; verified live against the staging PG, including
  the no-`providerSpecificData` case, where a COALESCE pair creates the
  object). The in-memory copy is updated with the same snapshot, so the
  resolving call itself already serves the pinned identity. A failed
  write is logged and ignored — the next persisting touch retries, and
  every surface derives from the same runtime in the meantime, so there
  is no observable divergence.

## Read rules

- ONE resolver family in the providers package:
  `AntigravityIdentityPinned(psd)` (pin or zero),
  `AntigravityIdentityForPSD(psd)` (pin or runtime),
  `AntigravityRuntimeIdentity()`. Surfaces resolve PER CONNECTION:
  chat forwarding + `ForwardGemini` fallback (the connection's identity
  is resolved once per request and threaded into the translator
  wrappers, so body `userAgent` == header UA), project discovery
  (loadCodeAssist/onboard metadata + UA), quota lanes (refresh/fetch
  carry the UA), media image/search/STT (identity resolved via the
  chat handler's exported accessor), dashboard usage + subscription
  info (metadata from the pin), dashboard models fetch, and the
  connection probe (identity resolved once, reused across its 401
  refresh retry).
- Surfaces with NO connection in scope keep the runtime derivation by
  design: the translator wrappers' default when handed an empty UA,
  and the providers-level helpers themselves
  (`AntigravityLoadCodeAssistMetadata()` now delegates to the runtime
  identity).

## §7 application (fragment 971 verdicts)

- **Scopes — APPLIED (CERTAIN, runtime capture).** The OAuth handler's
  scope list is now agy's captured order: cloud-platform,
  userinfo.email, userinfo.profile, cclog, experimentsandconfigs,
  aicode, `openid`. The only delta vs before is the added `openid` and
  the ordering.
- **Hosts — NO CHANGE.** The endpoint verdict is LIKELY-grade (daily),
  and JS parity keeps discovery/probe on prod; nothing moves on a
  LIKELY.
- **OAuth client — NO CHANGE.** The 971 runtime capture proved agy's
  interactive sign-in uses the router's current 1071 primary client.
  The previously planned flip to the 884354919052 client is
  RECLASSIFIED from a deferred parity step to an open question: its
  serving flow is unknown, and no flip happens unless new evidence
  names that flow. The fragment-970 dual-config rescue slot stays as
  the landing place if a second client is ever proven.

## Residual unknowns (carried from 971, unchanged)

- Whether the `RunLogin` configs carry the identical scope list is
  UNKNOWN (the capture covers the interactive sign-in flow only).
- The endpoint rule stays LIKELY, not CERTAIN (gated behind a completed
  Google sign-in capture).
- `pluginVersion`, `updateChannel`, and `duetProject` remain omitted
  from the loadCodeAssist metadata — unproven, never invented.
- agy's token fallback filename stays uncorroborated; the router stores
  tokens in the DB, so no surface depends on it.

## Verification

- `go build ./...` clean; `go vet` clean on providers, db, translator,
  proxy, and handlers (chat, oauth, dashboard, media). Mechanical
  call-site updates in existing tests for the two widened signatures
  (translator wrappers, quota functions); no test suites run.
- Boot smoke vs staging PG (`router9_staging`): `GET /api/version`
  200.
- The backfill UPDATE's SQL chain exercised directly against the
  staging PG in a rolled-back transaction: writes into a row with no
  pre-existing `providerSpecificData`, reads back verbatim, and a
  second write is a no-op (first writer wins).
