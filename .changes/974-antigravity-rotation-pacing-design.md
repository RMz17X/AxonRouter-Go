## Antigravity rotation pacing — mechanism C IMPLEMENTED (A and B declined)

Status: **C implemented** (2026-11-06, uncommitted); A and B remain
design only. Ordered on the anti-suspend track (2026-11-06) after
fragment 973: the goal is to remove the machine-like shape of pool
rotation, grounded in what the code does today — every behavioral
claim below is cited, and every number is labelled **DERIVED** (read
from observed quota/upstream data or an existing in-repo precedent)
or **PROPOSAL** (a value for the operator to pick; nothing in the
data picks it for us). The implementation record at the end of this
fragment states what actually shipped, with its parameters and
verification evidence.

### Current behavior (survey, 2026-11-06, main @ 94f17710 + fragment 973)

**Selection.** `ChatHandler.getBestConnectionWithContext`
(`internal/handlers/chat/connections.go`) loads the active rows only
(`Repo.GetProviderConnections(provider, true)` — `isActive = 1`;
`internal/db/connections.go`, order: priority ASC, NULLs last, then
`updatedAt DESC`), filters strict model assignment and per-model
eligibility, drops rows parked by `connectionModelBlocked` (per-model
lock or the in-memory quota cache) and rows whose
`db.ConnectionBlockedUntil` (the earlier of `rateLimitedUntil` /
`oauthLockedUntil`) is in the future, then takes the **first** survivor
of the strategy-ordered list. Order comes from
`applyConnectionStrategy` (`connections.go`): `round-robin`/`sticky` →
`selectByRecency` (persistent LRU with a consecutive-use sticky limit,
stamped via the row's `lastUsedAt`/`consecutiveUseCount`); `random` → a
random rotation; anything else — including **no strategy configured** —
keeps DB priority order, i.e. **fill-first: the top account absorbs
everything until it errors**. Strategy source is
`connRotationStrategy` (per-provider `settings.ProviderStrategies`,
falling back to the global fallback strategy unless it is `fill-first`).
Session affinity (affinity-eligible strategies only) pins a session to
one account and forces `StickyLimit = 1` for new sessions; the binding
is evicted on 429/401/403. A client-pinned connection is honored only
when eligible (`pinnedConnectionIneligible`); probes bypass cooldowns
and locks but never a disabled row.

**Account-state vocabulary** (all in the `providerConnections.data`
blob unless noted): `isActive` (column; 0 = disabled — fragment 973's
terminal state), `status`/`testStatus`, `lastError {status, message,
timestamp[, model, source]}`, `rateLimitedUntil` (account-scoped quota
cooldown), `backoffLevel`, `modelLock_<model>` timestamps (per-model
parks; Antigravity models canonicalized, e.g. the tiered flash pool),
`oauthLockedUntil` + `oauthFailureCount` (rejected-grant park),
`verificationUrl`, the `quotas` snapshot (last dashboard usage fetch;
deleted by status resets), `providerSpecificData.antigravityIdentity`
(fragment 972's pin), plus the row columns `priority`, `lastUsedAt`,
`consecutiveUseCount`.

**Failover on 429 QUOTA_EXHAUSTED, step by step.**
`handleAccountFallback` (`internal/handlers/chat/fallback.go`) walks
the strategy-ordered candidate list, one attempt per account,
**with no delay or jitter between hops** — hops are bounded only by
pool size. Each failure is classified by
`providers.ClassifyErrorForProvider` (`internal/providers/errorclassify.go`)
and turned into parked seconds by `cooldownSecForClassification`
(`internal/handlers/chat/retry_after.go`): a 429's own announced wait
wins (Retry-After header, `google.rpc.RetryInfo` `retryDelay`,
`quotaResetDelay`, "Resets in X"), clamped to `maxResetCooldown` = 72h;
quota-worded bodies take the fixed `QuotaExhaustedCooldownMs` = 1h;
a bare 429 takes exponential backoff, 2s base doubling to a 5min cap
(`GetQuotaCooldown`, backoff level persisted, max level 15). The writes
on the exhausted account (`Repo` in `internal/db/accounts.go`):
`LockConnectionModel` on the canonical **and** raw model keys, always;
then, if the error is not model-scoped (`isModelScopedError`),
`LockConnectionRateLimit` sets `rateLimitedUntil = now + cooldown` —
model-scoped errors only `RecordConnectionScopedError` and leave the
account selectable for other models. ToS-shaped 403s disable instead
(fragment 973). A later success clears locks, cooldown and backoff in
one guarded statement (`ClearConnectionSuccessState`). **Re-eligibility
is purely the stored timestamp passing** — nothing else clears a
cooldown early except a success on that account or an operator reset
endpoint. In parallel, the native Antigravity path
(`internal/handlers/chat/gemini_handler.go`) feeds 409/429s to
`HandleAntigravityQuotaError` (`internal/handlers/chat/antigravity_quota.go`),
which refreshes live quota (30s minimum refresh interval) and blocks
the model in the **in-memory** quota cache until the upstream's own
`resetAt`; a strike-breaker adds a 15min cache block after 3
marker-carrying 429s in 60s against an optimistic quota reading.
Combos (`handleComboFallbackWithVisited`,
`internal/handlers/chat/combo.go`) run the same per-member lock writes
and add the system's **only** inter-attempt delay: after a fully failed
pass, one repeat pass waits the earliest Retry-After — capped at 2s
when a 429 was involved, 8s otherwise — plus 100–400ms jitter
(`passRetry.wait` / `addRetryJitter` in `retry_after.go`). If every
account is in cooldown and the operator enabled force-fallback, the
soonest-to-reset account is served anyway
(`forceMinCooldownConnection`).

**Pacing-like machinery that already exists** (build on it, don't
duplicate): the cooldown/backoff system above; the OAuth rejection
park (5min doubling to 24h, `internal/db/accounts_oauth_lock.go`);
the fragment-968 background-refresh de-synchronisation (5min tick,
shuffled due order, accounts spaced 1.5s — 12s for sensitive providers
incl. Google — plus 0–50% jitter, `internal/proxy/oauth/background.go`);
the 55s usage cache and the 30s quota-refresh floor (background-call
pressure, not generation pacing); the strike-breaker; the seat attempt
budget (`SeatAttemptBudgetMs`, bounds attempt *duration*, not rate).

**What a cap could be measured against.** There is **no maintained
per-account request counter**. What exists: `usageHistory` rows per
served request (`Repo.InsertUsageHistory`, `internal/db/usage.go` —
timestamp, provider, model, connectionId, status, tokens), from which
per-account counts over any window are one aggregation away; the
per-request in-memory `FallbackAttemptRecorder` (attempt outcomes,
dies with the request); and the routing log
(`<dataDir>/logs/routing.log`, mirrored at boot in
`internal/app/server.go`) — observability, not a meter. The selection
hot path reads only the connection row plus in-memory caches, and
AGENTS.md §4.A/§4.G forbid a per-selection aggregate query on the
1-vCPU target: any cap therefore needs an **in-memory counter**
incremented on the attempt path, bootstrapped from `usageHistory` at
boot, with the DB remaining the audit trail rather than the meter.

### Mechanism A — switch pacing (minimum dwell after an exhaustion event)

- **Mechanics.** When an account-scoped quota lock is written during a
  client turn, record a pool-level `lastSwitchAt` (in memory, per
  provider+model family). The fallback loop — and new turns — may not
  *start* on a different account until `dwell` has elapsed since the
  exhaustion event; a turn that arrives early waits (bounded by the
  seat budget) or returns the 429 with `Retry-After` = remaining dwell.
  Today this hop costs 0ms (`handleAccountFallback`), which is the
  single most machine-like observable in the system: account B's first
  request lands in the same millisecond account A's quota died.
- **Parameters (all PROPOSAL).** `switchDwell` ∈ {5s, 15s, 30s, 60s};
  scope: quota-class locks only (never transient 5xx/auth parks);
  optional escalation ×2 per consecutive pool-wide exhaustion round,
  capped ×8. No value is derivable from quota data — the upstream
  announces resets, not switch etiquette.
- **Cost.** Added latency on exactly the turns that already failed
  once; zero cost on healthy turns. No failed requests added (the turn
  either waits or returns the same 429 it returns today).
- **Evidence.** For: the zero-delay hop is a proven fact of the current
  code, and dwell directly removes it. Against: Google observes
  per-account traffic, not the router's internal hop timing — the
  dwell is only visible as the gap between A's last request and B's
  first, and only under sustained load; under sporadic load it is
  inert. It shapes bursts, it does not reduce total per-account load.

### Mechanism B — per-account request pacing

- **B1, minimum interval.** A per-connection in-memory leaky bucket:
  generation attempts on one account start at least `minInterval`
  apart; a request that arrives early **waits** (spilling to the next
  account would defeat the purpose — that *is* rotation). Parameters
  (PROPOSAL): `minInterval` ∈ {0 (off), 500ms, 1s, 2s}. Cost: latency
  under concurrency, head-of-line blocking on a hot account. Evidence:
  weakest of the three — a real `agy` client can legitimately burst,
  and no observed Google threshold sits between the quota itself and
  human typing speed. Included for completeness, not recommended.
- **B2, rolling caps.** Per-connection counters (in-memory, per §
  "measured against" above): at `capHour` requests in a rolling hour
  or `capDay` in a rolling day, the account is treated as in-cooldown
  until the window slides. Parameters (PROPOSAL): `capHour` ∈ {60,
  120, 300}, `capDay` ∈ {500, 1000, 2000}. The only DERIVED anchor is
  negative: Antigravity's own limits are per-model remaining-% with
  an upstream `resetAt` plus weekly/session family windows (the quota
  cache in `antigravity_quota.go`) — Google already caps by quota, so
  a count cap is a blunter behavioral proxy ("look like one person's
  CLI"), not a quota-derived number, and it must be presented as such.
  Cost: stranded quota (an account parked by our cap while Google
  would still serve it), new counter state with boot reconciliation,
  and client-visible 429s whenever the whole pool is capped.

### Mechanism C — jittered re-eligibility

- **Mechanics.** A cooldown's stored expiry stays the floor, but the
  account becomes selectable at `expiry + rand(0, jitterWindow)`,
  drawn once per lock event (stored alongside the lock, or derived
  deterministically from connection id + expiry so no extra write is
  needed). Applies to `rateLimitedUntil`, per-model locks, and the
  in-memory quota-cache blocks alike.
- **Parameters.** `jitterWindow` ∈ {30s, 2min, 5min} or 10% of the
  cooldown capped at 5min — PROPOSAL values, but the *shape* is
  DERIVED from two in-repo precedents: fragment 968 already jitters
  background refresh by +0–50% for exactly this reason ("fixed spacing
  marches the whole fleet in lockstep, which is itself a detectable
  pattern", `internal/proxy/oauth/background.go`), and combo retries
  already add 100–400ms jitter (`retry_after.go`).
- **Cost.** Near zero: a bounded delay at a moment the account was
  unusable anyway; no request fails that wouldn't have. One
  interaction to respect in implementation: force-fallback reads the
  stored `until` to pick the soonest account — jitter must apply
  consistently there, or the operator's escape hatch silently bypasses
  it (acceptable if deliberate; must not be accidental).
- **Evidence.** For: pool-wide exhaustion is common (one workload
  drains accounts together), so today the whole pool re-enters at the
  exact upstream-announced reset instant — synchronized re-entry is
  both a visible pattern and self-defeating (the herd re-exhausts
  instantly). Against: none observed; the mechanism only ever delays
  eligibility that does not exist yet.

### Recommendation

Implement **C first, then A at the smallest dwell (5–15s)**, and defer
B entirely until there is evidence a per-account rate (not the quota
itself) is what Google reacts to: C removes the synchronized re-entry
pattern at ~zero cost using a jitter shape the codebase already
endorses, A removes the zero-gap switch — the other proven machine
signature — at the price of latency on already-failed turns only,
while B spends real quota and real complexity on thresholds no
observation supports yet. Scope all of it to `antigravity` first
(AGENTS.md §3 provider isolation); the shared selection layer takes it
as per-provider settings next to `ProviderStrategy`, so another
provider can opt in later without a second implementation.

### Non-goals and risks

- **Disabled is terminal for pacing.** Pacing reads only cooldown/lock
  fields; a disabled row (`isActive = 0`) is never in the candidate set
  and no pacing write may set `isActive`, clear `lastError`, or
  otherwise touch fragment 973's state. The precedent guard is
  `ClearConnectionSuccessState`'s C-4 rule: background clears must not
  flip a disabled row back to active.
- **One home, both loops.** Dwell and jitter must live in the shared
  selection/failover layer, not be re-implemented per loop — the
  `cooldownSecForClassification` comment records what happens when
  `fallback.go` and `combo.go` each keep half the semantics. Combo
  second-pass wait behavior stays unchanged.
- **Small pools degrade to no-ops.** With fewer than 2 eligible
  accounts (the staging pool is 4; a 1-account pool is legitimate),
  switch dwell is a no-op (there is no switch), and caps must never
  park the *last* eligible account unless the operator explicitly
  opts into hard caps — default degradation is serve-and-log.
- **Probes stay exempt.** Pinned probes exist to test reachability and
  start quota windows; they already bypass cooldowns and must bypass
  pacing too (they still refuse disabled rows).
- **Hot path stays write-light.** All pacing state is in-memory plus
  the lock writes that already exist; no new per-request DB write and
  no per-selection aggregate query (AGENTS.md §4.A/§4.G).

### Decisions for the operator

| Parameter | Candidates | Kind |
|---|---|---|
| `switchDwell` (A) | off / 5s / 15s / 30s / 60s | PROPOSAL |
| dwell escalation | off / ×2 per round, cap ×8 | PROPOSAL |
| `jitterWindow` (C) | 30s / 2min / 5min / 10% of cooldown ≤ 5min | PROPOSAL (shape DERIVED) |
| `minInterval` (B1) | off / 500ms / 1s / 2s | PROPOSAL |
| `capHour` / `capDay` (B2) | 60/120/300 per h; 500/1000/2000 per day | PROPOSAL |
| hard caps on last account | serve-and-log (default) / hard park | PROPOSAL |
| scope | antigravity only (recommended) / all OAuth providers | PROPOSAL |

### Implementation record — mechanism C only (2026-11-06)

**Decision.** Ricki, 2026-11-06 16:17: implement **C only** — "C aja
dulu (jitter kelayakan), tanpa jeda pindah". Mechanism A (switch
dwell) and mechanism B (per-account pacing/caps) are **declined**;
their sections above stay as the design record, unimplemented. This
supersedes the recommendation paragraph, which proposed C + A.

**What shipped.** When an account-scoped cooldown is written for an
antigravity connection, the same UPDATE also stores a re-eligibility
companion — `rateLimitedEligibleAt` beside `rateLimitedUntil`,
`oauthLockedEligibleAt` beside `oauthLockedUntil`
(`internal/db/accounts_pacing.go`). The announced expiry is never
rewritten, so the dashboard keeps rendering the truthful upstream
reset; selection honors the companion. `db.ConnectionBlockedUntil`
now answers with each park's effective instant (the companion when
it is later than its own park, else the announced expiry), which
puts every eligibility consumer on the same jittered instant with
no per-consumer change: the selection filter and the cooldown error
message in `getBestConnectionWithContext`, the session-affinity
bound-connection check (a binding whose account sits in its jitter
tail reads ineligible and is evicted, so the re-pick also honors
jitter), `pinnedConnectionIneligible`, the combo candidate filter,
and force-fallback's soonest-to-reset pick — the escape hatch
compares jittered instants, so it cannot silently bypass the
mechanism. When every account is parked or in its jitter tail, the
existing pool-exhausted behavior is unchanged (the earliest
eligibility instant is what the cooldown error now reports); no
waiting loop was added anywhere.

**Stability.** The roll happens **once, at lock write time**, inside
the two writers (`Repo.LockConnectionRateLimit`,
`Repo.RecordConnectionOAuthFailure`) and is persisted in the same
statement as the park itself. Successive selection checks read the
stored instant and always agree — no per-check re-roll, no flicker,
stable across restarts. A lock written with jitter off (or for a
provider outside the scope) stores a **null** companion, retiring
any roll an older episode left behind; the success/reset clears
(`ClearConnectionRateLimit`, `ClearConnectionSuccessState`,
`ClearConnectionOAuthLock`, `ResetConnectionHealthState`, and the
activation branch of `SetConnectionStatus`) null the companions
alongside their parks. A companion that is not later than its own
park is inert by construction, so a dashboard reset that clears a
park but leaves a companion behind (the reset paths rewrite the
blob field by field) cannot extend a later park: the next lock
write overwrites the companion atomically anyway.

**Parameters (final).**
- Setting: `ProviderStrategy.CooldownJitterSec`
  (`providerStrategies.<provider>.cooldownJitterSec` in the settings
  row, beside the other per-provider strategy fields).
- Unset (the default): the derived window — `rand(0, 10% of the
  cooldown being written)`, capped at 5 minutes. **No floor**: the
  design names no floor for the proportional shape (the 30s figure
  was a fixed-window candidate, not a floor), so short parks get
  proportionally small jitter — a 2s bare-429 backoff rolls within
  0.2s. Rolled with `math/rand/v2`, the jitter source the existing
  precedents use.
- Explicit `0`: jitter off — selectable at the announced expiry,
  exactly the pre-974 behavior.
- Positive `N`: a fixed `rand(0, N seconds)` window replaces the
  derived one.
- Scope: the derived default applies to **antigravity only** (the
  writer resolves the connection's provider and canonicalizes it).
  Another provider gets jitter only when an operator explicitly sets
  a positive value on its own strategy entry — the opt-in door the
  design asked to leave open, with the default blast radius kept at
  one provider. The writer's provider/settings reads are two
  primary-key/settings-row lookups on the lock-write (failure) path;
  the selection hot path gained no query — readers parse the blob
  they already hold.

**Deliberate exclusions.**
- **Per-model locks (`LockConnectionModel`) are NOT jittered.** C as
  decided covers the account-scoped parks behind
  `ConnectionBlockedUntil`. Model locks are written on every
  classified failure — including purely model-scoped errors, where
  the account stays selectable for other models — and are mirrored
  into the speed-layer overlay keyed by the exact announced
  timestamp; the synchronized re-entry C targets is pool-wide
  *account* re-entry at the announced reset instant, and when an
  error is account-scoped the account park written beside the model
  lock carries the jitter. Model locks are the named follow-up if
  per-model re-entry sync ever becomes the observable.
- **The in-memory antigravity quota cache**
  (`chat/antigravity_quota.go`) is NOT jittered. Its blocks are
  model-scoped (same family as the model locks above), and its
  `resetAt` values are live upstream quota readings that each
  refresh replaces wholesale (30s refresh floor) — a stored jitter
  offset would be discarded and re-rolled per refresh, violating
  the roll-once stability requirement. The strike-breaker keeps its
  own semantics.
- **Background refresh keeps reading the raw OAuth park**
  (`ConnectionOAuthLockUntil`). Refresh is a repair path, not
  generation selection: a refresh that succeeds inside the jitter
  tail clears the park early, which is the documented
  `ClearConnectionOAuthLock` behavior ("a repaired credential does
  not stay out of rotation"), and the strike counter keeps keying
  on the raw park exactly as before. Fragment 968's refresh
  *scheduling* jitter is untouched.
- **Quota scoring keeps reading the raw `rateLimitedUntil`**
  (`chat/tokenmaxxing.go` synthesizes its rateLimited quota window
  from the announced reset): it derives a quota window from the
  upstream announcement, it is not an eligibility gate.
- **Fragment 973 is untouched**: no 973 file was modified, pacing
  never reads or writes `isActive`/disabled state, and a disabled
  row is never in the candidate set pacing could affect.

**Files.** `internal/db/accounts_pacing.go` (new: window resolution
+ the write-time roll), `internal/db/accounts_oauth_lock.go`
(`ConnectionBlockedUntil` folds the companions via
`parkEffectiveUntil`; `RecordConnectionOAuthFailure` and
`ClearConnectionOAuthLock` write/clear the OAuth companion),
`internal/db/accounts.go` (`LockConnectionRateLimit`,
`ClearConnectionRateLimit`, `ClearConnectionSuccessState`,
`ResetConnectionHealthState`), `internal/db/dashboard.go`
(`SetConnectionStatus` activation clears the OAuth companion),
`internal/db/settings.go` (the `CooldownJitterSec` field + its
deep-copy in `cloneProviderStrategies`).

**Verification.** `go build ./...` clean; `go vet` clean on
`internal/db`, `internal/handlers/chat`, `internal/handlers/dashboard`,
`internal/proxy`; `gofmt` clean on all touched files. Direct checks
against the staging PostgreSQL (synthetic connections, removed
after; no test suites run): an antigravity 1h rate-limit lock stored
a companion +10s inside the [expiry, expiry+5m] window and
`ConnectionBlockedUntil` returned exactly the stored companion; a
legacy blob without a companion read back the raw expiry unchanged;
a first-strike OAuth park (5m) stored a companion +15s inside
[expiry, expiry+30s]; a non-antigravity lock stored **no**
companion and read back raw; the clear paths retired both parks
and both companions. Boot smoke: the built binary booted against
the staging database and `GET /api/version` answered 200.
