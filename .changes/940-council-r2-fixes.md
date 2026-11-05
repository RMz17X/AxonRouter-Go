# Council round 2 — fix pass (F-B1, F-B2, F-B3, R2-S1, R2-S2, R2-N1, plus NITs R2-C-N1, R2-C-N2, verification NIT-1)

Fixes for the findings in `~/workspace/9router-council-r2-{fidelity,security,correctness,verification}.md`,
applied on main @ 05ac2184. Fidelity failed with 3 BLOCKING (B-1, B-2,
B-3), security with 2 BLOCKING (R2-S1, R2-S2) plus the R2-N1 product
bug it found on the security path; correctness and verification
passed. Everything below is one serialized pass; no test suites were
run (this round's suite evidence is round 1's), verification is direct
checks only: build, vet, gofmt, and live probes on scratch databases.

## Verification budget (stated before the runs)

- `go build ./...`, `go vet ./...`, `go vet -tags integration ./...`
  green; `gofmt` clean on every touched file. No `go test` anywhere.
- Probe A (gates), fresh DB + fresh boot, requireLogin on, engine key
  + dashboard session as the two caller classes: zed auto-import 401
  key-only and handler-reached for the session; the four LOCAL_ONLY
  routes (cursor/kiro/zed auto-imports, cowork-settings) 403 with
  Axon's exact body for a session arriving via-proxy, unaffected
  locally; /api/models 401 key-only and HEAD key-only, 200 `{models}`
  for the session, /v1/models 200 key-only (untouched); settings
  writes 401 key-only with the settings row verified unchanged in the
  database, session PUT of an ordinary key 200 and persisted, a
  session PUT planting a proxy token stripped; anonymous credentials
  export still 401; a usage-carrying internal report with the boot's
  own report token 200 with the usage row landed, wrong token 401;
  reset-password (refactored onto the shared wrapper) 403 via-proxy
  and 200 locally.
- Probe B (vault), vault-enabled boots with a canary sealed under K1:
  key-only rotate 401; session rotate to K2 200, rewrapped=1; restart
  with K2 and the canary decrypts through the operator export.
- Battery sanity (the preserved fix_probe.py rig, fresh DB + boot):
  300 chats @ 50 workers, exact reconciliation, dropped counter 0.

## F-B1 — zed auto-import joins the always-protected list

`internal/middleware/dashboard_auth.go` (`IsAlwaysProtectedPath`):
`/api/oauth/zed/auto-import` added beside its cursor/kiro siblings,
completing Axon's three (dashboardGuard.js names all three). The
route returns a live Zed IDE access token from the OS keyring; an
engine-key-only caller got 200 before, 401 now (probe A); a session
caller still reaches the handler (200 with the handler's own body).

## F-B2 — the LOCAL_ONLY layer reaches Axon's other four paths

`internal/handlers/batch7_auth.go`: the reset-password handler's
inline check is now a shared wrapper, `RequireLocalOnlyRoute` (same
`canAccessLocalOnlyRoute` core, same flat 403 body
`{error:"Local only: CLI token required"}`), and reset-password is
refactored onto it — behaviour unchanged, probe-verified both ways
(403 via-proxy, 200 local). `internal/handlers/router.go` applies the
wrapper at registration to the three auto-imports
(`mountOAuthRoutes`, which now takes the repo) and to
`/api/cli-tools/cowork-settings` — Axon's five LOCAL_ONLY paths
(dashboardGuard.js) are now all covered. Composition note: Axon runs
the check in its guard before routing; Go composes it at
registration behind the dashboard gate, the composition the
reset-password port established in batch 7 and round 2's fidelity
lens verified as faithful. Probe A: a session caller presenting as a
non-local peer (the `x-axonrouter-via-proxy` header — the same
evidence the reset-password port keys on) gets Axon's 403 on all
four routes; the same caller locally is unaffected. A true
non-loopback socket-peer leg remains environment-blocked in this
sandbox (fake-IP egress accepts no inbound), as round 1 recorded
for S-4; the check keys on the socket peer exactly like the
already-proven reset-password path.

## F-B3 — /api/models takes its dashboard caller class

`internal/handlers/router.go`: the bare `/api/models` GET (both
copies) and HEAD registrations are removed from the engine group and
registered once in `SetupDashboardRoutes` as
`RequireOperatorCaller(repo, chatH.HandleModels)` (GET + HEAD). The
handler already serves Axon's dashboard `{models}` shape on this
spelling (batch 9); only the caller class was inverted — the engine
gate refused the session Axon admits and admitted the engine key
Axon refuses. The operator wrapper is the catalog-sync idiom from
round 1 (N-5) composed with the dashboard gate, which reproduces
Axon's guard class exactly: session, CLI token, or a login-off
install; an engine key alone is rejected by the wrapper. The engine
group's `/api/models/*` lookup and the `/v1/models` surface are
untouched. Probe A: key-only GET 401, HEAD 401, anonymous 401,
session 200 with the `{models}` shape, key+session 200, /v1/models
key-only still 200.

## R2-S1 — the settings write side is operator-class, and the proxy tokens are write-stripped

`internal/handlers/router.go`: all four settings write registrations
(PUT/PATCH `/api/settings` and the PUT/PATCH `/settings` aliases) now
take `RequireOperatorCaller`. The generic update merges caller JSON
into the settings row, which carries the security configuration
itself (requireLogin, ipWhitelist, trustedProxyEnabled) — the lens
chained a key-only write of those keys to an anonymous credentials
export. Probe A: key-only PUTs planting the token pair, rewriting
the security keys, and hitting the alias all answer 401 with the
settings row verified byte-unchanged in the database; the chain-B
sequence dies at step 1 (anonymous export still 401); an operator
session PUT of an ordinary setting answers 200 and persists.

Defense-in-depth, evaluated separately: `internal/handlers/dashboard/settings.go`
adds `internalProxyResolveToken` and `internalProxyReportToken` to
`protectedSettingKeys` (the write-strip list), mirroring their
read-strip place since round 1's S-3. Nothing legitimate writes the
pair through this handler — the worker-proxy handlers mint and
persist them directly against the repo (`FillSettingsRawIfAbsent`)
and no rotation endpoint exists — so the strip cannot break the
token lifecycle; probe A confirms a session PUT carrying a planted
token answers 200 with the planted value absent from the row. The
configuration keys (requireLogin, ipWhitelist, trustedProxyEnabled)
were deliberately NOT added to the strip list: the operator manages
them through this very settings surface, so stripping them would
break the legitimate settings UI — they are the operator-class
question, and the gate is their control.

## R2-S2 — vault rotation is operator-class

`internal/handlers/router.go`: `POST /api/vault/rotate` takes
`RequireOperatorCaller`. The handler takes the new master key from
the request body and re-wraps every connection DEK under it; an
engine-key-only caller could destroy the sealed credential store
irreversibly. Probe B (vault-enabled, canary sealed under K1,
plaintextCount 0, canary absent from the stored row): key-only
rotate 401 `{error:"Unauthorized"}`; session rotate to K2 200 with
rewrapped=1, failures=[]; after a restart with K2 the operator
export returns the canary — the rotation is real for the operator
and unreachable for the engine key. (The tests-only
`dashboard.RegisterRoutes` table keeps its bare registration: it is
not mounted by the server, and the wrapper lives in the handlers
package it cannot import.)

## R2-N1 — usage-carrying internal reports no longer 500

`internal/handlers/batch7_internal_report.go` (`saveReportUsage`):
the `meta` argument to `InsertUsageHistory` was `""`, and
`usageHistory.meta` is JSONB — PostgreSQL rejects the empty string
(SQLSTATE 22P02), failing every usage-carrying report before the
detail write. It now passes `"{}"`, an actual empty JSON document.
Probe A: with the boot's own minted report token (64 hex chars), a
report carrying usage answers 200 `{"ok": true}` and the ledger
gains exactly one row whose meta reads `{}`; a wrong token still
answers 401. (Before the fix this leg 500'd, which had accidentally
been masking the forged-usage half of R2-S1's chain A; the sabotage
half is closed by the gate above, not by this bug.)

## NITs landed in the same pass

- R2-C-N1 (dead folds deleted): `internal/handlers/chat/usage.go`
  loses `upsertDailyUsage` and `upsertDailyUsageError` (~55 lines)
  and the now-unused `database/sql` import; the merge helpers stay
  (live via `applyDailyDeltas`), and the stale comments in usage.go
  and `usage_daily.go` no longer speak of a direct one-shot call.
  Reference check before deletion: the only callers anywhere were
  the quarantined `usage_latency_probe_test.go`
  (`//go:build sqlite_legacy`). That fleet does not compile under
  any tag set — `go vet -tags sqlite_legacy ./internal/handlers/chat/`
  fails on a pre-existing `undefined: db.OpenDatabase`, unrelated to
  this deletion — so no buildable tag set references the wrappers;
  the probe will call the accumulator when the fleet is ported
  (decision D1). Build and vet are green on the default and
  integration tag sets.
- R2-C-N2 (warning order): `internal/config/config.go`
  (`loadJWTSecret`) now generates the ephemeral secret first and
  fires the once-per-process warning only when one was actually
  generated, so a `crypto/rand` failure leaves the Error line as
  the whole (truthful) report instead of a warning claiming the
  opposite.
- Verification NIT-1 (doc definition): DATABASE.md §6's
  `usageWritesDropped` definition now covers both counter sources —
  ledger rows that could not land AND generic-lane jobs
  (compression-analytics records) shed at a full queue — so it can
  over-report ledger loss but never under-reports it, matching
  `UsageWritesDropped`'s own comment in usage_writer.go.

## Probe outcomes (all on the fixed tree, scratch DBs dropped after)

- Probe A: 28/28 legs PASS (script `~/tmp-smoke/r2fix_probe_a.py`).
- Probe B: 9/9 legs PASS (script `~/tmp-smoke/r2fix_probe_b.py`).
- Battery sanity: 300 @ 50 → 0 errors, history Δ +300, details
  Δ +300 exact, `usageWritesDropped` 0, zero drop lines in the
  gateway log, p95 1,135 ms (inside the 1,500 ms budget).
- Build + vet green on default and `-tags integration`; gofmt clean
  on all nine touched files. No gateway processes remain; the
  staging inventory is back to the standing fixture databases.

Not in scope and untouched, per the round's record: fidelity N-1
(shadowed duplicate registrations), N-2/N-3/N-4/N-5, security
N2/N3, and the opencode sync-tokens FLAG (owner's).
