# Council round 1 — fix pass (S-1, S-2, S-3, S-4, C-1, N-2, N-3, N-5, fidelity NIT-1, C-2, C-3, C-4, verification F-4, plus the fold-accumulation defect the C-1 probe exposed)

Fixes for the findings in `~/workspace/9router-council-r1-{security,correctness,fidelity,verification}.md`,
applied on main @ b83b18ff. Security failed with 4 BLOCKING (S-1…S-4),
correctness with 1 BLOCKING (C-1), verification with 1 BLOCKING (F-1 —
the integration suite's first execution, which the parent runs
separately at this gate; it is not part of this pass). Fidelity passed.
Everything below is one serialized pass; no test suites were run (the
parent owns the suite run), verification is direct checks only: build,
vet, gofmt, and live probes on scratch databases.

## Verification budget (stated before the runs)

- `go build ./...`, `go vet ./...`, `go vet -tags integration ./...`
  green; `gofmt` clean on every touched file. No `go test` anywhere.
- Probe A (gates + settings + parity), fresh DB + fresh gateway boot,
  canary credential seeded, two caller classes (engine key only /
  dashboard session): export 401 key-only and 200 with the canary for
  the session; import 401 key-only; both MCP audit routes 401
  key-only; GET /api/settings 200 with none of the three secret keys
  present for either caller; both chrome-import routes 401 key-only
  (kiro auto-import as the unchanged control); catalog-sync POST 401
  key-only and non-401 for the session; models/info missing-id 400
  byte-compared against Axon's document.
- Probe B (C-1), fresh DB + fresh boot, dedicated non-superuser role
  for the gateway: seed the day's totals through real chats, revoke
  SELECT on `usagedaily` from the gateway's role, fire chats, assert
  the seeded totals survive untouched, assert the Error log line for
  the aborted fold, restore SELECT, assert the retained deltas then
  land on top of the seeded totals.
- Probe C (S-4), fresh boot without ALLOW_PRIVATE_NETWORKING: a local
  caller still probes a loopback MCP server successfully; a caller
  disqualified as non-local (via-proxy header) gets 400 for loopback
  and localhost URLs. The rebinding/redirect legs cannot be staged in
  this sandbox (fake-IP DNS, read-only /etc/hosts — the same limit
  the security lens recorded); they rest on the composition of two
  already-proven primitives (resolving AssertPublicURL, dial-time
  SafeTransport), stated as such.
- Probe D (battery sanity, battery-spec discipline: fresh DB + fresh
  boot): 300 chats @ 50 workers against the 500 ms mock, reconciliation
  exact +300/+300, `usageWritesDropped` 0.
- Teardown: scratch DBs dropped, gateway processes killed by exact
  PID, probe role dropped.

## S-1 (BLOCKING) — credentials export/import now require an operator-class caller

The two existing gates composed into a full credential dump:
`RequireDashboardAuth` admits any engine API key and
`RequireManagementAuth` admits any whitelisted source address (the
default whitelist covers the whole 192.168/16 LAN), so a client key on
any LAN machine could export every provider credential in plaintext —
or replace them via import.

- New wrapper `RequireOperatorCaller` (internal/handlers/batch7_auth.go):
  the caller must satisfy `middleware.CallerIsDashboardPrivileged` —
  a dashboard session, the local CLI token, or a login-off install —
  the idiom the F-6 fix established for the API-key list. Failure is
  the same flat 401 `{ "error": "Unauthorized" }` the management gate
  answers.
- Applied at registration, inside the existing management gate, to
  every route sharing that exact gate composition: GET
  /api/credentials/export, POST /api/credentials/import, and the MCP
  audit pair (GET /api/mcp/audit, GET /api/mcp/audit/stats) — the
  audit routes return the tool-call record (input hashes, output
  summaries, API-key ids) and share the identical composition, so
  they take the identical caller class. Session and CLI-token callers
  keep working in exactly the shapes they did before; only the
  engine-key class is removed.

## S-2 (BLOCKING) — Chrome-import routes are always-protected

POST /api/providers/gemini-web/import-chrome returns the operator's
live Google session as a cookie header and its DeepSeek twin returns
the account's userToken plus cookie header, to any dashboard-gate
caller — including any engine API key, from any network. Both paths
join `IsAlwaysProtectedPath`, matching the cursor/kiro auto-import
precedent for the same host-secret class (Axon's own list predates
these two routes; the class match is the codebase's own standard).

## S-3 + N-3 (BLOCKING + NIT) — settings reads never serve the internal-proxy tokens or the legacy MITM value

`secretSettingKeys` was `["password"]` only, so GET /api/settings
served `internalProxyResolveToken` and `internalProxyReportToken` to
any dashboard-gate caller; the report token is a write primitive
(forged usage rows, marking connections auth-blocked). All three keys
— both tokens plus the legacy `mitmSudoEncrypted` (no code reads it;
a legacy row can still carry it) — are now in the single-sourced
strip list. Consequences, all intended: `sanitizeSettings` (GET +
update responses) and `stripSecretSettings` (database backup export)
drop them; `readSettingsSecrets` preserves the live values across a
backup import, exactly like the password. The internal-proxy handlers
read the row directly via `GetSettingsRaw` and are unaffected
(verified in code: batch7_internal_proxy.go never touches the
sanitized view).

## S-4 (BLOCKING) — the Cowork MCP probe is no longer an SSRF lane

For non-local callers the probe checked only the media package's
literal-host guard and then fetched with a plain `&http.Client{}` —
no resolution check, no dial-time guard, unchecked redirects — so a
name resolving to a private address, or a public URL redirecting to
one, reached internal services. The probe now composes the product's
own standard: the literal check stays as the fast path,
`handlerutil.AssertPublicURL` resolves and rejects private results,
and the fetch runs through a dedicated guarded client built on
`proxy.NewSafeTransport()` (dial-time revalidation, rebinding-proof)
with a `CheckRedirect` that re-runs `handlerutil.AssertPublicURL` on
every hop (10-hop cap). The local-caller branch is byte-for-byte the
old behaviour — plain client, loopback targets allowed — so an
operator probing their own MCP servers from the dashboard keeps
working. The registry fetch (fixed Anthropic URL, not
caller-supplied) keeps the plain client, as before.

## C-1 (BLOCKING) — the daily-aggregate fold is failure-atomic per date

All three fold sites discarded the `GetUsageDaily` error, so a
transient read failure folded onto an empty base and the full-row
upsert destroyed the day's committed totals (fault-injection proof in
the correctness report: a seeded 1000-request day read back as 2).

- Accumulator (usage_daily.go): `applyDailyDeltas` now distinguishes
  `sql.ErrNoRows` (the legitimate empty base) from any other read
  error — and from marshal/upsert failures — and returns the deltas
  that did not land; `flushDailyUsage` puts them back on the pending
  list instead of having swapped it to nil up front (the F-4 half of
  the finding). A retained backlog is capped at 100,000 deltas with
  an Error log on truncation, so a sustained outage cannot grow it
  without bound; the ledger is unaffected either way.
- One-shot folds (usage.go, success and error): a non-ErrNoRows read
  error logs at Error and skips the upsert. The error fold's upsert
  error, previously discarded with `_ =`, is now logged too.
- No blob whose base read failed is ever upserted, at any site. The
  file header's "can be refolded from the ledger" sentence — a
  capability that does not exist in code — is corrected to state the
  retention guarantee instead.
- Explicitly not a redesign: the multi-process fold window (C-6,
  documented single-writer limitation) is untouched.

## Fold accumulation defect (found by the C-1 probe, same fold, fixed in this pass)

Probe B's seed phase failed its first assertion: three chats folded
to requests=1. A sequential diagnostic (distinct token counts per
chat: 132/200/300) plus PostgreSQL statement logging isolated it:
every fold read the correct base and batch, the floats accumulated
(final cost = the three chats' sum), but every integer counter ended
at the LAST delta's values. Root cause: the merge functions store Go
`int` results back into the aggregate map (`data["requests"] =
getJSONInt(...) + 1`), while `getJSONInt` accepted only `float64` —
the type unmarshal produces. A second delta in the same batch, and
any in-memory re-read, therefore saw its own stored counts as 0.
Single-delta folds land correctly by accident (0 + the delta), and
the ledger-based batteries never read the blob across a multi-delta
fold, which is why it survived every earlier check; the council's
own C-1 proof is consistent with it. `getJSONInt`/`getJSONFloat`
now read any numeric representation the map can hold (new
`jsonNumberAsFloat` helper, usage.go) — the getters are used only by
the daily-fold machinery and its tests. After the fix the diagnostic
reads requests=3, promptTokens=632, and probe B's seed lands 3/396.
This widens C-1's repair only inside the fold it already covers: a
fold that cannot accumulate is not failure-atomic in any useful
sense, and probe B's brief ("a normal fold still lands deltas")
cannot pass without it.

## N-2 (NIT) — constant-time compares on the two internal gates

`hasValidInternalAuth` (batch7_internal_proxy.go) and the
`AXONROUTER_PEER_TOKEN` check in `isDirectPrivateRequest`
(batch7_auth.go) now use `subtle.ConstantTimeCompare`, matching the
standard the session and CLI-token code already holds.

## N-5 (NIT) — catalog-sync aligned to Axon's caller class

Divergence found: in Axon, `/api/models/catalog-sync` falls under the
guard's deny-by-default `/api/*` rule — admitted callers are a
dashboard session, the CLI token, or a login-off install; engine API
keys are consulted only for the public `/v1` prefixes and never admit
here (dashboardGuard.js; the route file itself carries no auth because
the guard owns it). The Go port had both verbs in the bare
`RequireApiKey` group, so any engine key could trigger the sync's
outbound fetches. Both registrations now additionally take
`RequireOperatorCaller`, landing on Axon's caller class; the GET is
wrapped with the POST because Axon gates the pair identically.

## Fidelity NIT-1 — /v1/models/info missing-id 400 is Axon-verbatim

The 400 is now hand-built like the 404 (`writeModelInfoMissingID`):
message `Missing required query param: id (e.g. ?id=openai/dall-e-3)`
— with the space — type `invalid_request_error`, and no `code` key
(the shared WriteJSONError added one). With both error documents
hand-built, the file header's "Axon's error documents … are served
verbatim" claim now holds for the whole route (the per-key access
gate's own error remains gateway policy, as the header states).

## C-3 (NIT) — deterministic tiebreaker on the shared details select

`requestDetailListSelect` now sorts `timestamp DESC, id DESC`.
Timestamps are millisecond strings and same-millisecond rows are
routine under burst, so the old sort left page boundaries over ties
arbitrary for both consumers of the shared select (the Details list
and the usage export walk).

## C-4 (NIT) — the fused success clear no longer resurrects a disabled connection

`ClearConnectionSuccessState` stamped `status: "active"` whenever its
guard matched, so a request in flight while the operator disabled its
connection silently undid the disable's presented status when its
success clear landed. The stamp is now a CASE in the same single
statement: it lands only when the row is not operator-disabled —
`isActive = 1`, no `disabledAt` stamp, `testStatus` not "disabled",
the same triple `flattenConnection` derives the row's state from —
and a disabled row keeps its stored status while its locks,
cooldowns and error fields clear exactly as before. The
cycle-2-verified field union is otherwise identical. (Statement-shape
note: the CASE never yields SQL NULL — a NULL jsonb_set value would
null the whole document — so the disabled-and-statusless corner
preserves as an explicit JSON null, which every consumer reads as
"no status".)

## C-2 (NIT, comment only) — the settings write lock stops overclaiming

The `settingsWriteMu` comment now states the lock is per-process and
names the cluster residual (two nodes merging concurrently can drop
each other's keys), mirroring the honest note
`FillSettingsRawIfAbsent` already carries for its narrower case. No
behaviour change.

## Verification F-4 (docs) — the dropped counter's boundary caveat

DATABASE.md §6's `usageWritesDropped` line gains the clause: the
counter covers losses while the process runs; a shutdown that cannot
drain within its grace window logs the fact and the tail is lost
uncounted. (The accumulator half of F-4 is fixed in code under C-1
above.)

## Probe results

Build/vet/gofmt: `go build ./...`, `go vet ./...`, and
`go vet -tags integration ./...` all green; `gofmt -l` clean on all
14 touched files (the tree carries pre-existing unformatted files
elsewhere — untouched, out of scope). No `go test` was invoked.

Rig notes: (1) every probe boot sets `JWT_SECRET` — with it unset,
sessions never validate: `config.LoadConfig()` is rebuilt per call
and mints a fresh ephemeral secret per call, so login's signing
secret differs from the verify secret and /api/auth/session answers
`invalid_signature`. Pre-existing on main, unrelated to this diff
(config.go/session.go untouched); flagged for the parent as a
possible defect in the default install shape. (2) The local staging
PostgreSQL was down after a platform restart and its keeper cannot
restart it from this shell (the sandbox denies the keeper's
`nobody`-uid traversal of /home/hatch; cluster data intact, stale
postmaster.pid removed). Probes therefore ran against a scratch
PostgreSQL 17.11 cluster under /tmp on the same port 15432, removed
at teardown.

Probe A (gates + settings + parity): ALL PASS (17/17). Key-only:
credentials export 401, import 401, MCP audit 401, audit stats 401;
GET /api/settings 200 for both caller classes with
internalProxyResolveToken, internalProxyReportToken and
mitmSudoEncrypted absent from the parsed keys and the raw body
(settings row planted with all three via SQL between boots);
gemini-web and deepseek-web import-chrome 401 (kiro control also
401); catalog-sync POST+GET 401 key-only, POST admitted for
key+session (503 = handler entered, sandbox egress fails the fetch);
session export 200 with the seeded canary credential present;
session MCP audit 200; models/info missing-id 400 byte-exact
against Axon's document.

Probe B (C-1): ALL PASS (5/5). Seed totals landed requests=3
promptTokens=396 (this assertion is what exposed the fold
accumulation defect above — it failed at requests=1 before that
fix). Fault phase: chats succeed (200/200) while SELECT on
usagedaily is revoked from the gateway's role; the seeded totals
SURVIVE the revoked-read folds (still 3/396); the Error line "read
daily usage failed; fold aborted, deltas retained" is present in
the gateway log; after GRANT restores SELECT, the retained deltas
land (requests=5, promptTokens=660). Probe-shape note: ownership of
the scratch DB's tables was transferred to the gateway role before
the revoke, because boot-time schema DDL (CREATE INDEX) requires
ownership — the revoke still denies the fold's read exactly as the
lens's shape intends.

Probe C (S-4): ALL PASS (4/4). Local caller probes the loopback MCP
mock successfully (200, tools=['mock_tool']) — the feature is
intact; a via-proxy (non-local) caller gets 400 "URL not allowed"
for both the 127.0.0.1 and the localhost URL forms; a non-local
caller with a public literal URL is answered through the guarded
fetch (error surfaced, no tools). Rebinding/redirect legs as
budgeted: not stageable in this sandbox, resting on the composed
primitives.

Probe D (battery sanity): reconcile exact — history_delta=300,
details_delta=300, expected=300; usageWritesDropped=0; log drop
lines 0/0; 300 @ 50 workers, 0 errors, p95 1076 ms against the
500 ms mock (inside the battery spec's 1,500 ms budget).

Sequential-fold diagnostic (the accumulation defect): before the
getter fix, 3 chats (132/200/300 prompt tokens) → requests=1,
promptTokens=300, cost = the 3-chat sum; after → requests=3,
promptTokens=632, ledger=3.
