# Review cycle 1 — fix pass (F2, F1, F3, M1, M2, m1–m3)

Fixes for the findings in `~/workspace/9router-review-cycle1.md`, applied
on main @ 56da2f6e. Every fix was verified by direct checks only (build,
vet, live probes) — no test suites were run, per the standing rule.

## F2 (BLOCKING) — the usage ledger no longer drops rows

`internal/handlers/chat/usage_writer.go` was rewritten as two lanes:

- **Ledger lane (new, never droppable in practice).** usageHistory and
  requestDetails inserts travel as one job per request on a dedicated
  queue. The single worker accumulates them and writes multi-row
  INSERTs (`db.InsertUsageHistoryBatch` / `InsertRequestDetailBatch`,
  200-row chunks in `internal/db/usage.go`) at 128 rows or a 25 ms
  timer, so drain throughput is bounded by batched inserts, not by one
  round trip per row. Enqueue blocks with a 10 s bound
  (`ledgerEnqueueTimeout`) instead of dropping; a drop is only
  reachable when the database has been unwritable for ten continuous
  seconds, and is then counted and logged as before. The request
  goroutine never touches the DB — worst case it waits out a stalled
  worker rather than losing the row.
- **Generic lane (unchanged contract).** The pre-existing 256-deep
  droppable queue now carries only compression analytics, whose
  fire-and-forget contract the code already documented. Its drop
  behaviour (and the test pinning it) is untouched.
- **Daily aggregate left the per-request path.** `logUsage` /
  `LogFailure` now fold into an in-memory delta accumulator
  (`internal/handlers/chat/usage_daily.go`), flushed every 3 s and on
  shutdown: one settings read-modify-write per flush window instead of
  one ~21 ms RMW per request. Failed-request folds take the same path.
- **Observability.** `GET /api/system/memory` now reports
  `usageWritesDropped` beside the `speedLayer` block
  (`internal/handlers/batch7_system.go`), wired to the existing
  `chat.UsageWritesDropped()` counter, which now also counts the
  post-timeout ledger drops.
- `FlushUsageWrites` keeps its contract and now also barriers the
  ledger queue and flushes the daily accumulator against the same
  deadline, so shutdown drains both lanes.
- **Follow-up found by the probe (same finding, second channel):** the
  request-detail ID was `"<unixMilli>-<model>"`, so requests landing
  in the same millisecond with the same model minted identical IDs
  and the detail insert's `ON CONFLICT DO NOTHING` silently ate the
  duplicates — with the queue fixed, history reconciled exactly while
  details still landed 375 short at 100 workers (a signature the
  original review capture also shows: its tables diverged 353 vs 296).
  `newUsageRequestID` (`internal/handlers/chat/usage.go`) keeps the
  timestamp-model shape and adds a process-atomic sequence plus a
  random tail (cross-node collisions), at both minting sites
  (`logUsage`, `LogFailure`). After the fix the probe reconciles
  exactly on both tables.

## F1 (MAJOR) — hot-row UPDATE serialisation removed

1. **Success-path clears are conditional and fused.**
   `Repo.ClearConnectionSuccessState` (`internal/db/accounts.go`) does
   the work of `UnlockConnectionModel` ×1–2 plus
   `ClearConnectionRateLimit` as ONE UPDATE whose WHERE guard only
   matches when clearable state exists (a model lock under either key,
   `rateLimitedUntil`, `lastError`, or a raised `backoffLevel` — every
   writer of that state raises one of them). With nothing to clear —
   the common case — the statement matches zero rows: primary-key
   probe, no row lock, no WAL record. `fallback.go`'s success path
   calls it once. The overlay lock entries are still deleted
   unconditionally, exactly as `UnlockConnectionModel` did. The
   standalone clear/unlock methods are unchanged for their other
   callers (media handlers, dashboard unlock, deepseek renewal).
2. **API-key usage accounting is accumulated.**
   `internal/middleware/apikey_usage.go`: the auth path now only
   increments an in-memory per-key counter; a flusher lands each key's
   count as one `Repo.AddApiKeyUsage` UPDATE per 10 s interval, and
   `app.Server`'s OnStop flushes after `FlushUsageWrites`. Bounded
   loss statement: a hard crash loses at most one 10 s interval of
   `usedCount`/`lastUsedAt` increments. Those two fields are resale
   bookkeeping, never an authorisation input; the usage ledger itself
   is F2's domain and is not affected.
3. **Settings row read is cached.** `internal/db/settings.go` serves
   the raw settings row from a 5 s TTL cache; `updateSettings` forces
   a fresh read for its read-modify-write base and refreshes the cache
   with the exact bytes it wrote (write-through), so the hot
   `logUsage` path stops paying a settings SELECT per request while
   writes through the Repo never merge into a stale base. Two writers
   bypass the Repo (provider rename tx, settings import); their writes
   are bounded by the same 5 s TTL.
4. **Deprecation probe short-circuit.** `internal/db/deprecations.go`
   keeps an in-memory set of providers that have any deprecation row;
   `GetModelDeprecation` answers `not deprecated` without a query for
   providers outside the set. The set loads lazily, grows on
   `RecordModelDeprecation`, and is invalidated on
   `ClearModelDeprecation`; a failed load or an unknown provider
   state always falls back to the real query.
5. **Rider m1 — connection snapshots cached.**
   `GetProviderConnections` (`internal/db/connections.go`) serves a
   1 s TTL snapshot, deep-cloned on the way in and out (the selector
   mutates its rows). Every connection writer through the Repo
   invalidates the snapshot immediately (create/update/delete/status/
   priority/data, locks, cooldowns, OAuth parks, health resets); the
   rotation stamp writer patches snapshots in place instead, so
   round-robin ordering inside the window stays exactly as fresh as
   the persisted stamp. Writers that bypass the Repo (raw-SQL OAuth
   handlers, a second process, the shared dashboard) are bounded by
   the 1 s TTL — a just-set cooldown can be invisible to selection
   for up to a second; the fallback loop still catches the failure
   and locks then, so correctness holds and only the first stray
   request pays.

## F3 (MAJOR) — compose pool surface

Both compose files: the app service passes `PG_POOL_MAX_OPEN`
(default 10) and `PG_POOL_MAX_IDLE` (default 5); the postgres service
runs with `command: postgres -c max_connections=${PG_MAX_CONNECTIONS:-100}`.
`.env.example` documents all three knobs; `DATABASE.md` §4 states the
invariant: **APP_REPLICAS × PG_POOL_MAX_OPEN + 10 (migrator/operator
headroom) ≤ PG_MAX_CONNECTIONS** — raising one side alone previously
failed closed with SQLSTATE 53300 under load.

## M1 (MAJOR) — banned-account cooldown semantics reconciled

One place owns the semantics now, commented at
`internal/providers/errorclassify.go` and
`internal/handlers/chat/retry_after.go`:

- The classifier marks the banned-account class
  (`ErrorClassification.BannedAccount`): the ToS-violation rules and
  the two 404 text rules whose cooldown IS `cooldownBannedAccount`.
- `cooldownSecForClassification` returns that class's duration
  UNCLAMPED (the origin commit's 24 h stands); every other class
  keeps flowing through `retryableCooldownSec` exactly as before
  (2 h generic ceiling, deepseek-web 72 h, antigravity unchanged).
  Both cooldown call sites (fallback, combo) use the wrapper.
- `isModelScopedError` carves `requested entity was not found` OUT of
  the blanket 404 model-scoping rule, so that class takes the
  account-level `LockConnectionRateLimit` path; plain `model_not_found`
  404s stay model-scoped, now with the unclamped 24 h.
- **Rider m2:** the two banned-account TEXT rules carry
  `RequireStatus: 404`, so a 429 (or any non-404) whose body quotes
  `model_not_found` can no longer mint a 24 h ban. The ToS text rules
  deliberately stay status-free (providers return them at several
  statuses).

## M2 (MAJOR) — integration harness ported to PostgreSQL

- New reusable helper `internal/db/dbtest`: `NewSchemaHandle(prefix)`
  resolves `INTEGRATION_DATABASE_URL` (fallback `DATABASE_URL`),
  creates a fresh schema in that database, and returns a `*sql.DB`
  pinned to it via pgx `search_path`, plus a cleanup that drops the
  schema. Per-Env isolation is per-Env SCHEMA in one shared scratch
  database (documented in the package comment); the D1 fleet port
  builds on this helper.
- `internal/integration/harness_test.go` provisions through it;
  `db.OpenDatabase` (removed in P4) is gone from the tree's test
  bootstrap. The 300-file `sqlite_legacy` fleet was NOT ported in
  this pass (per instructions).
- The same vet acceptance exposed a second stale bootstrap the
  review's pass did not reach (vet reports per package):
  `internal/integration/bootfx/main_test.go` still set the removed
  `config.Config.DatabasePath`. It now provisions through
  `dbtest.NewSchemaDSN` and hands the schema-pinned DSN to the app's
  own pool via `DATABASE_URL` — the only database source the tree
  has left — dropping the schema after the fx app stops.
- `AGENTS.md` §5.D's harness status note updated to the new fact.

## m3 (MINOR) — speed-layer breaker trips on flapping primaries

`internal/speedlayer/failover.go`: the consecutive-failure counter is
replaced by a sliding window — the breaker opens at
`failThreshold` (3) primary failures inside a 10 s window regardless
of interleaved successes; successes only let failures age out. The
prober heal path is unchanged apart from clearing the window.

## Probe budget (stated BEFORE the run)

Rig: the reviewer's shape (`~/tmp-smoke/rev_probe.py`) — this tree's
binary against a scratch database on the local staging PostgreSQL,
threaded mock upstream with a fixed 500 ms delay, streaming chat
completions, one seeded connection + API key.

- Load: **2,000 requests at 100 workers** through the gateway, plus a
  direct-to-mock control at the same shape.
- **Usage reconciliation EXACT:** after the load and a drain wait,
  `usageHistory` and `requestDetails` each gain exactly +2,000 rows
  over their pre-load counts.
- **Zero** `dropping usage row` (generic lane) and zero
  `usage ledger write queue full` (ledger lane) warnings in the
  gateway log.
- **Latency:** gateway p95 ≤ control p95 + 500 ms.
- Functional probes: `GET /api/system/memory` shows
  `usageWritesDropped`; a mock 404 with a `model_not_found` body
  produces a ~24 h model lock on the connection (and no account
  cooldown); a mock 404 with a `requested entity was not found` body
  produces a ~24 h account-level `rateLimitedUntil`.
- `docker compose config` parses both compose files (run on agent-x,
  which has the CLI; this VM does not).
- `go build ./...` and `go vet ./...` green on default tags AND with
  `-tags integration`.

If the local rig cannot sustain 100 workers faithfully, the largest
faithful scale is run instead and the substitution is stated here.

## Probe results (run 2026-11-05, this tree's binary, local staging PG)

- **Load:** 2,000 requests at 100 workers through the gateway, **0
  errors**, wall 17.8 s. p50 828 ms, **p95 1,111 ms**, max 1,529 ms.
  Control (same client direct to the mock): p95 2,548 ms → budget was
  p95 ≤ 3,048 ms. **PASS**, ~1.9 s under budget — and against the
  review's own numbers (p95 3,786 ms at only 50 workers; P8's
  6,233 ms at 100 workers) the hot-row serialisation is gone: the
  gateway now beats the raw control, whose tail is the local Python
  harness's own connection churn (34/1000 control requests errored
  direct-to-mock; the gateway arm had none).
- **Reconciliation: EXACT.** usageHistory +2,000, requestDetails
  +2,000 over the closed window (baseline 1/1 from the seed warmup).
  Zero `dropping usage row` and zero `usage ledger write queue full`
  warnings in the gateway log. (First run with only the queue fixed:
  history exact, details +1,625 — that gap was the ID collision
  documented in the F2 follow-up above, not a drop.)
- **Observability:** `GET /api/system/memory` returns 200 with
  `"usageWritesDropped": 0` present beside `speedLayer`.
- **M1 locks:** mock 404 `model_not_found` → `modelLock_gpt-4o-mini`
  set exactly 24 h out, `rateLimitedUntil` null. Mock 404
  `requested entity was not found` → `rateLimitedUntil` set exactly
  24 h out (the by-design per-model lock accompanies it, per
  fallback.go's documented "account-scoped cooldown alongside the
  per-model locks"). Both durations unclamped (previously 2 h).
- **Compose:** `docker compose config` parses both files (run on
  agent-x — this VM has no docker CLI); the cluster render shows the
  pool passthrough and `-c max_connections=200` from stub env.
- **Build/vet:** `go build ./...` green; `go vet ./...` green on
  default tags AND with `-tags integration` (the integration tree
  did not compile at all before this pass).
- Probe databases and processes were dropped/killed after the run;
  the rig is preserved at `~/tmp-smoke/fix_probe.py`.
