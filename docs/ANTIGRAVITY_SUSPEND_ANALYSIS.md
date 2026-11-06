# Antigravity Suspension — Root-Cause Analysis (Jajang's version)

Collected 2026-11-06 by Jajang. Purpose: one evidence base to summarise into the
"make AxonRouter-Go safe for Antigravity" patch plan. Every item is graded:

- **VERIFIED** — read directly in this tree (`main @ 5ee02c10` + the 6-file
  uncommitted hijilaptop patch), in the hijilaptop production DB/logs, or in a
  primary web source.
- **REPORTED** — claimed by ErNor App's Discord analysis (2026-11-06 05:37 WIB)
  but not (yet) confirmed in this tree.
- **HYPOTHESIS** — plausible, unmeasured.

Reference trees: Go = this repo. JS = AxonRouter (`open-sse/`, axonrouter17).

---

## 0. The umbrella cause (VERIFIED, web)

Clause 6 of the Google Antigravity Additional Terms: using third-party
software/tools/services to access the Service (example given: OpenClaw with
Antigravity OAuth) is a breach; penalty is suspension/termination of the
Antigravity and/or Gemini CLI accounts. Google ran a documented ban wave
against exactly this pattern in February 2026, and its forum staff state they
suspend accounts that extract/proxy OAuth tokens or automate multi-account
rotation.

Consequence: **no router usage is ever "compliant".** The JS version was not
safe because it was legal; it was safe because its fingerprint was stable and
quiet enough to stay under the detection threshold. Everything below is about
detection surface, not legality.

Proven casualty in our own data (hijilaptop DB, read 2026-11-06): 1 of 4 local
accounts hard-disabled with Google's verbatim error `403 … This service has
been disabled in this account for violation of Terms of Service …
PERMISSION_DENIED TOS_VIOLATION`; the other 3 fell at the same time to
429 QUOTA_EXHAUSTED (x2) and 404 NOT_FOUND locks — the whole pool unusable.

## 1. One account, many identities (VERIFIED in this tree)

The same Antigravity token reaches `cloudcode-pa.googleapis.com` with a
different identity depending on which subsystem calls:

| Caller | Identity sent | Source |
| :--- | :--- | :--- |
| Chat | `antigravity/cli/1.4.0 (aidev_client; os_type=windows; arch=amd64; …)` — hardcoded | `internal/proxy/gemini.go:106` |
| Media (image/search/STT) | same CLI UA, hardcoded | `internal/handlers/media/antigravity_*.go` |
| Dashboard usage | CLI UA **plus** `X-Client-Version: 2.23.0` (the IDE version) — CLI and IDE identities mixed in one request; daily host | `internal/handlers/dashboard/usage_providers.go:1693,1712-1720` |
| Project discovery | profile-aware: `ide` → `antigravity/2.23.0` (bare), `cli` → CLI UA; metadata numeric `{ideType:9, platform:2, pluginType:2}` | `internal/handlers/chat/antigravity_project.go:26-57` |
| Connection probe | `loadCodeAssist` body with **string** enums `{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}` | `internal/handlers/dashboard/connection_probe.go:49` |

Supporting defects:

- `AntigravityIDEUserAgent = AntigravityCLIUserAgent` — the IDE identity is
  aliased to the CLI one (`internal/providers/client_identity.go:45`), so even
  profile-aware callers cannot present a real IDE identity.
- The profile system itself is instance-wide / per-connection stored
  (`internal/providers/antigravity_profile.go`), i.e. chat-path identity is
  stable per account — the inconsistency is **between subsystems**, not random
  per request. (This corrects ErNor's "OS randomised by hash per call" framing
  for this tree.)
- JS contrast: one profile chosen at OAuth time, stored permanently, and the
  same `buildAntigravityHeaders` used for chat and usage for the account's
  whole life (`open-sse/providers/registry/antigravity.js`,
  `open-sse/services/usage/google.js`). Chat UA there is the IDE identity
  `antigravity/ide/2.11.0 darwin/arm64` (`open-sse/providers/shared.js:101`).

Why it matters: Google's abuse detection runs on the backend. A token whose
OS/client/version story changes between calls looks shared or botted — the
exact profile the Feb-2026 wave targeted. The probe's string-enum body is the
same mismatch class that the 9router upstream issue #270 diagnosed as flagging
accounts on their first `loadCodeAssist` call.

## 2. Background traffic bypasses the account's proxy (VERIFIED for refresh)

- Background OAuth refresh runs every 5 minutes
  (`internal/proxy/oauth/background.go:20`) and refreshes with a bare
  `&http.Client{Timeout: 15s}` (`background.go:183`) — **no account proxy, no
  proxy pool**. Every account on an instance refreshes from the server's own
  IP. On dewacloud that was ~19 accounts from one datacenter IP (account count
  REPORTED by ErNor; the refresh mechanism and cadence are VERIFIED here).
- Dashboard usage fetches share `usageHTTPClient`, which wraps only the
  environment transport in `proxy.FallbackTransport`
  (`internal/handlers/dashboard/usage_providers.go:180-188`) — environment
  proxy at best, **never the account's configured proxy pool**.
- JS contrast: token refresh and usage checks run inside
  `runWithProxyContext(extractProxyOptions(credentials))` per connection
  (`open-sse/services/tokenRefresh.js`, `src/app/api/usage/[connectionId]/route.js`).

Effect: accounts that the operator deliberately separated by proxy collapse
back into one egress cluster for all background traffic — one flag sweeps the
cluster. This is the strongest "why ALL accounts at once" mechanism.

Mitigations already present in this tree (do not re-add): refresh is
sequential with inter-account spacing (12 s + jitter for sensitive providers),
and a dead refresh grant now parks + disables the connection
(`background.go` refreshBackgroundConnection; ErNor's 5ee02c10 line).

## 3. No server-side usage cache (VERIFIED by absence)

No usage cache exists in `internal/handlers/dashboard/usage*.go` (searched:
no cache map/TTL). Every dashboard open/refresh hits
`fetchAvailableModels` + `loadCodeAssist` live, per account. JS has
`USAGE_CACHE_TTL_MS = 60_000` plus in-flight de-duplication
(`src/app/api/usage/[connectionId]/route.js:16-43`), capping Google hits at
1/minute/account regardless of UI polling.

## 4. Flagged accounts kept being hit — FIXED, do not regress (VERIFIED)

Before ErNor's patch line, a TOS/validation-flagged account kept `isActive=1`,
so the 5-minute refresher and rotation kept presenting a dead/banned token to
Google — the escalation pattern (repeat hits lengthen locks). Current state:
403 TOS_VIOLATION / validation / dead grant → status `disabled`, `isActive=0`,
OAuth auto-refresh stopped, appeal surface in UI (commits e18c30e1, fb584e06,
5ee02c10 + the uncommitted probe-persistence patch synced 2026-11-06).

## 5. Rotation behaviour reads as quota farming (VERIFIED behaviour)

The fork's own features — instant rotation on definitive quota errors
(QUOTA_EXHAUSTED → next account immediately), round-robin across the pool,
24 h locks on 404 — are individually reasonable gateway behaviour, but across
a pool they produce the "automated multi-account rotation" pattern Google's
staff name as enforced. Combined with §2's shared egress, rotation spreads a
flag instead of containing it.

## 6. Two instances ran the same accounts simultaneously (VERIFIED, now resolved)

Until 2026-11-06 ~11:25 WIB the same accounts were live on dewacloud
(production, datacenter IP) and on hijilaptop (staging, direct egress) at the
same time — hijilaptop logs show successful chat traffic and
`BG_TOKEN_REFRESH` for the same connections while production served the same
pool. One token active from two IPs/machines is a sharing signal on its own.
Resolved by the operator's decision the same day: `axon.hijitoko.com` now
serves the JS version again (verified externally 11:29 WIB) and Go runs only
on hijilaptop for testing.

## 7. Claims checked and NOT found in this tree (corrections to ErNor's analysis)

1. **Hardcoded foreign project fallback `bamboo-precept-lgxtn`** — not present.
   `internal/handlers/chat/antigravity_project.go` has no default project ID;
   on "no project" it returns `noProject`, caches the miss for 10 minutes and
   logs onboarding guidance. ErNor's file/line references
   (`antigravity_project.go:48,145`) do not match this tree (320 lines,
   different layout) — he analysed a different checkout. Do not patch what is
   not here; keep a regression watch instead.
2. **"Add X-Goog-Api-Client to loadCodeAssist for consistency"** — this tree
   deliberately sends **none** on discovery calls, pinned by
   `antigravity_project_test.go` (expects the header empty), matching the JS
   note that the real IDE does not send it and Google fingerprints it to
   refuse project provisioning
   (`open-sse/config/appConstants.js`: `ANTIGRAVITY_LOAD_CODE_ASSIST_HEADERS`).
   Consistency work (§1) must align UA + metadata, not add this header to
   discovery.

## 8. Hypotheses (unmeasured)

- **TLS/HTTP fingerprint**: Go `net/http` presents a different TLS (JA3/JA4)
  and HTTP/2 profile than Node's stack; if Google scores it, header parity
  alone will not close the gap. No measurement exists yet.
- **Datacenter IP reputation**: dewacloud egress is a datacenter range;
  residential/local egress (hijilaptop) is plausibly scored lower-risk
  (ErNor's conclusion #1). Consistent with the timeline — the ban landed while
  the pool ran on dewacloud — but not isolated from §1/§2 as a variable.

## 9. Timeline (all 2026-11-06, WIB)

- ~02:27 — TOS_VIOLATION hit recorded (cooldown until 19:27 UTC in hijilaptop DB).
- 05:03 — ErNor commit 5ee02c10 (auto-disable + appeal + stop refresh on disabled).
- 05:07–05:41 — Discord: disable report, ErNor App fingerprint analysis (05:37),
  instructions to fix per JS pattern and harden further.
- 05:51–05:54 — Operator orders: stop Go on dewacloud, repoint domain to JS
  (axonrouter17, old DB), Go only on hijilaptop for testing.
- 06:12–07:00 — ErNor App stalls (output-token limit, then provider-down loops).
- 11:15 / 11:25 — "Lanjutkan"; ErNor resumes on a fallback model; by 11:29 the
  domain verifiably serves the JS version.
- 11:24–11:31 — Code synced hijilaptop → this VM; this analysis collected and
  verified against the synced tree.

## 10. Open verification gaps

- Dewacloud pool size and per-account errors (no DB access from here; the
  "~19 accounts" figure is ErNor's).
- Whether the connection probe honours the account proxy in all paths (probe
  code carries proxy fields; not every probe call site was traced).
- Which exact call produced the first TOS_VIOLATION (no request-level log kept
  from the dewacloud instance).

## 11. Resolution (2026-11-06, Jajang — patches landed in working tree, uncommitted)

Corrections to earlier sections, verified against the synced tree and
Ricki's local JS clone (~/workspace/AxonRouter):

- The connection probe is NOT a Go defect: the JS test utilities send a
  byte-identical loadCodeAssist body and User-Agent. The probe was left
  unchanged (JS parity). Any text above flagging the probe body as a
  fingerprint risk is superseded by this note.
- X-Goog-Api-Client stays absent from loadCodeAssist discovery on
  purpose (pinned by antigravity_project_test.go; the real IDE does not
  send it, and Google fingerprints clients that do).
- No "bamboo-precept" fallback project ID exists in this tree; that
  claim came from a different tree and does not apply here.

Landed in the VM working tree (go build + go vet clean; boot smoke
against the local staging PostgreSQL passed; .changes/968):

- Patch A — per-profile User-Agent across chat, media lanes, dashboard
  usage (incl. weekly overlay and subscription-info) and models fetch.
- Patch B — account-proxy egress for background OAuth refresh and
  dashboard usage fetch/refresh (connection binding, then provider
  strategy pool; direct only as fallback, JS strictProxy=false parity).
- Patch C — 55-second per-connection cache on GET /api/usage/{id};
  ?force=1 bypasses and refreshes.
- Patch D — IDE profile identity un-aliased from the CLI identity.
- Patch E — no change needed: the chat classifier already treats
  404/NOT_FOUND as model-scoped with a 1-day model cooldown, never an
  account cooldown (verified in the fallback classifier).
- Patch F — rotating routing log file under the data dir
  (logs/routing.log, 20 MiB x 5; AXONROUTER_LOG_FILE overrides,
  "off" disables), wired at server boot.
- Quota tracker: name/email search (server-side `search` parameter)
  plus per-account and reset-all cooldown buttons via the bulk reset's
  cooldown action (disabled accounts untouched).
- Provider detail page: quotas load on demand (Load quotas button)
  instead of one live usage fetch per connection on page open.

The svelte-check ratchet reports 89 errors vs its pinned baseline of
80 on this working tree; line-level attribution shows none of the 89
sit on lines added by these patches — the growth pre-dates them
(ErNor's uncommitted edits in the same tree). The baseline was not
raised.

ErNor's pending Discord queue was recovered in full from the Hermes
session store on hijilaptop (~/.hermes/state.db); the texts and their
dispositions are recorded in MEMORY.md. Commit, push and deploy await
the operator's order; staging verification is docker compose on
hijilaptop per the standing rule.

## 12. Follow-up audit (2026-11-06, after the patches landed on staging)

A second pass over the patched tree, hunting anything else that makes
an account look like a bot fleet member rather than a client:

- FIXED — refresh cadence: the background loop used fixed inter-account
  delays (1.5s normal / 12s sensitive) in a stable DB order every tick,
  while its own comment claimed jitter that did not exist. The due list
  is now shuffled per tick and each gap jittered +0-50%.
- VERIFIED OK — project discovery: runs only when no project ID is
  stored, the result is persisted, and a definitive no-project answer
  is negative-cached; steady-state chat makes zero discovery calls.
- VERIFIED OK — endpoints: chat, usage and media go to the daily host,
  discovery stays on prod, exactly like the JS build.
- VERIFIED OK — chat headers: the JS build sends only a User-Agent as
  its custom header; the Go chat path matches after Patch A.
- PARITY, LEFT ALONE — the one-time OAuth code exchange egresses from
  the server IP with a plain client in BOTH builds (server-side fetch
  in JS); it is not a differentiator. The steady-state refresh grant,
  which was the differentiator, is Patch B.
- NOTED — every account egresses through one provider proxy group
  (the operator's own provider-lock design): shared fate by choice.
  Per-account proxy diversity would spread that risk; it is an
  operations decision, not a code defect.
- NOTED — pinned client versions (CLI 1.4.0 / IDE 2.23.0) are frozen,
  as in the JS build (IDE 2.11.0 there). Real clients auto-update, but
  a mixed-version fleet would be a bigger anomaly than a uniform one.
- OPEN, DECISION PENDING — TLS fingerprint: antigravity traffic uses
  Go's net/http TLS stack, whose ClientHello is a known automation
  signature and matches neither the Electron IDE (Chromium) nor a
  Node-based CLI. The repo already vendors bogdanfinn/tls-client with
  Chrome profiles for its browser-cookie providers, but antigravity's
  chat path is net/http throughout, so adopting it means an fhttp
  adapter on the hot path. Risk is hypothesis-level (never isolated as
  the ban trigger; the proven triggers were identity mixing and proxy
  bypass). Recommended shape if adopted: opt-in per provider, tested
  on one account before any fleet rollout.

## 13. The agy rebase supersedes IDE parity (2026-11-06, Jajang — working tree, uncommitted)

Ricki's order after §§11–12: stop mirroring the IDE and mirror the real
standalone client. The official `agy` CLI v1.5.1 binary was torn down
with REA (identity spec graded CERTAIN/LIKELY/UNKNOWN in
~/workspace/agy-analysis/AGY-IDENTITY-SPEC.md), and the router's
Antigravity identity was rebased onto it wholesale (.changes/969).
This section supersedes the IDE-parity parts of the §11 resolution:

- Patch A's mechanism — a per-connection client profile (CLI by
  default, IDE when chosen) driving the User-Agent on each surface —
  is GONE. There is no profile any more: `antigravity_profile.go` is
  deleted, and a `clientProfile` value stored on an old connection is
  inert legacy data that nothing reads. Every surface presents the one
  CLI identity, because several client shapes for one account was the
  fingerprint drift §1 indicted.
- Patch D (IDE identity un-aliased, `antigravity/2.23.0`) is moot: the
  IDE identity no longer exists in the tree. The single definition is
  `internal/providers/antigravity_identity.go`:
  `AntigravityUserAgent()` builds agy's runtime grammar
  (`antigravity/cli/1.5.1 (aidev_client; os_type=<runtime GOOS>;
  arch=<runtime GOARCH>; cl=1012564305; auth_method=consumer)` — the
  changelist is the one agy 1.5.1's own build metadata carries; the old
  UA was a frozen 1.4.0-era Windows capture), and
  `AntigravityLoadCodeAssistMetadata()` returns the numeric
  ClientMetadata (ideType 9 ANTIGRAVITY, pluginType 2 GEMINI,
  ideVersion "1.5.1", ideName "antigravity", platform derived from the
  same runtime OS/arch). pluginVersion / updateChannel / duetProject
  are omitted: the teardown could not establish their values, and an
  omitted field is honest where an invented one is itself a
  fingerprint.
- Consumers rewired to that one definition: chat forwarding and the
  ForwardGemini fallback, project discovery (loadCodeAssist +
  onboardUser), the chat quota lanes (which sent a stray
  `antigravity/1.0.0` UA plus an X-Client pair), the translator's
  chat and image wrappers (body `userAgent` now equals the header UA),
  media image / search / STT (X-Client pair removed; body `userAgent`
  aligned), dashboard usage + weekly overlay + subscription info
  (X-Client pair removed; metadata via the shared builder), the models
  fetch, and the connection probe.
- The probe correction in §11 is itself superseded for antigravity:
  the probe's string-enum loadCodeAssist body was JS parity, but agy
  sends the numeric shape, and Ricki's order is agy parity — the
  antigravity probe branch now sends the numeric metadata body and the
  CLI UA. The gemini-cli probe branch is untouched. X-Goog-Api-Client
  stays absent on loadCodeAssist (the §11 reasoning stands: the real
  client does not send it there).
- X-Goog-Request-Reason is RESOLVED (code-read of the agy binary,
  2026-11-06): agy sends the header only when a host-set runtime
  string is non-empty; in the standalone CLI flow it stays empty, so
  the header is effectively absent — exactly like the JS build, which
  never sends it. The router faithfully omits it.
- The one open residual is the OAuth client swap. agy signs in with
  its 884354919052 client; this router still signs in and refreshes
  with the pre-rebase 1071006060591 client (restored in
  providers.KnownOAuthConfigs; the sign-in handler now seeds from that
  one home instead of a duplicated literal, and the scope list gains
  https://www.googleapis.com/auth/aicode, the CLI's distinctive
  scope). The swap is deferred by Ricki's decision — the secret
  transfer path is closed on this machine — with the design ready:
  when the secret lands, the 8843 config becomes primary and the 1071
  config becomes a fenced refresh-rescue fallback in a dual-config
  refresher (new client first, one legacy attempt on 401 or 400
  invalid_client/invalid_grant), because existing accounts' refresh
  tokens are client-bound to the client that minted them. If a refresh
  ever failed under both clients, the account would need a fresh
  sign-in; nothing else changes.
- Still open from the teardown (§7 of the spec): the endpoint
  selection rule (daily vs prod per surface — current hosts are
  unchanged by this rebase), the exact sign-in scope list, the token
  file format, and the updater TTL.

### §13 addendum — the dual-config refresher lands (2026-11-06, fragment 970)

The "design ready" above is now built. `AntigravityLegacyOAuthConfig`
gives the pre-swap 1071 client a named, fenced home (refresh rescue
only — sign-in and code exchange still seed from `KnownOAuthConfigs`
alone), and `internal/proxy/oauth/antigravity.go` registers the
dual-config refresher the design described: primary first, exactly one
legacy attempt on a client-mismatch rejection (401, or 400
`invalid_client` / `invalid_grant`), the primary error surfaced when
the rescue also fails, and the rescue skipped outright while both
slots name the same client — so until the flip, wire behavior is
identical to the standard refresh it replaces. The OAuth residual is
therefore no longer a code change: when the 8843 pair lands (env
override or literal), the primary slot flips and the architecture is
complete. See `.changes/970-antigravity-dual-config-refresher.md` for
the full record.
