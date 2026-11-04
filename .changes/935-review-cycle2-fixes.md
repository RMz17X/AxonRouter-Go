# Review cycle 2 — fix pass (N1, N2, N3, N4, N5, N7, N8)

Fixes for the findings in `~/workspace/9router-review-cycle2.md`, applied
on main @ 433d9d05. Every fix was verified by direct checks only (build,
vet, live probes) — no test suites were run, per the standing rule. N6
(the opencode-sync token FLAG) is explicitly out of scope: it is
parity-exact with Axon and awaits the owner's decision, unchanged.

## N1 (MAJOR) — ledger batch failures are counted, and cost one row, not a batch

`flushLedgerBatch` (`internal/handlers/chat/usage_writer.go`) treated a
failed batch statement as terminal: the run was logged and discarded,
and `usageWritesDropped` never moved — the counter only counted
queue-full drops, so under a fast-failing database the ledger lost rows
silently at drain speed, and one poisoned record could take a whole
128-row run with it.

- Each lane's flush now goes through `insertLedgerHistoryBatch` /
  `insertLedgerDetailBatch`. On a batch error the batch failure is
  logged with its row count (as before), then the run falls back to
  per-row inserts (single-record batch statements, so the writer's own
  SQL — including the detail lane's `ON CONFLICT DO NOTHING` dedupe
  grain — is unchanged). One bad row now costs one row.
- Every row that still fails per-row increments `usageWritesDropped`,
  so the number in `GET /api/system/memory` means ALL ledger rows
  lost, which is what F2's observability requirement asked for.
- The fallback runs only on the failure path; the happy path stays one
  statement per run. Worker runs never exceed one of the batch writer's
  internal chunks (`ledgerBatchMax` 128 < `usageInsertChunk` 200), so a
  batch statement is atomic and the fallback can never re-land a row
  the batch already wrote.
- The queue semantics are untouched: the ledger lane still blocks with
  its 10 s bound, the compression (generic) lane is untouched.

## N2 (MINOR) — the deprecation provider set reloads on a TTL

`deprecationProviderHas` (`internal/db/deprecations.go`) loaded the
provider set once per process: a deprecation recorded by another node
(or the shared dashboard) for a provider absent from the loaded set
was never learned, so this node's `clearModelDeprecation` short-circuited
forever and the dashboard badge stayed stale until restart.

- The set now carries its load time (`Repo.deprecationProvidersAt`,
  `internal/db/repos.go`) and reloads once older than
  `deprecationSetTTL` = 5 minutes. The fail-open behaviour is
  unchanged: a failed load (initial or reload) answers true, so the
  real query runs — the set may only ever skip a query it is sure is
  empty.
- The TTL is deliberately looser than the sibling caches (settings
  5 s, connections 1 s): the set guards a display-truth signal, not a
  routing input (selection never consults deprecations), and the
  reload is one scan of a tiny kv scope. Five minutes bounds
  cross-node staleness to minutes at no measurable cost (§4.G: every
  cache gets a bound; this was the one cache in the cycle-1 fix
  without one).

## N3 (MINOR) — HEAD answers on the /v1 read surface

`HEAD /v1/models` returned 405 while GET answered 200: chi routes per
method and nothing registered HEAD. Axon runs on Next.js, which serves
HEAD from the GET handler; SDK health checks, load balancers and
client libraries probe with HEAD and read this gateway as down.

- `SetupRoutes`' router interface gains `Head`, and HEAD is registered
  alongside GET for the whole model-catalogue read family with the
  SAME handlers: `/models`, `/models/info`, `/models/{kind}`,
  `/models/*`, and the `/v1`, `/api/v1`, `/api` spellings of the models
  list and lookup, plus `/v1beta/models` and `/audio/voices` (the
  `/v1/audio/voices` spelling folds to it, as it does for GET).
- The handlers are shared, not reimplemented: the net/http server
  discards a HEAD response's body itself, so status and headers —
  including Content-Length — are GET's by construction.
- OPTIONS is deliberately left as-is: Axon adds no CORS preflight
  either, so the posture stays parity (per the review).

## N4 (MINOR) — a 429 can no longer mint a banned-account lock via ToS text

The three ToS text rules (`terms of service`, `tos_violation`,
`disabled in this account`) were status-free and evaluate before the
429 backoff rule, so a 429 quota body quoting a ToS phrase classified
BannedAccount → 24 h unclamped → account-level lock — the hazard m2
closed for the 404 spellings, left open for these.

- `ErrorRule` gains `ExcludeStatus`, the mirror of `RequireStatus`,
  and the three ToS rules carry `ExcludeStatus: 429`. The match loop
  skips a text rule at its excluded status, so a 429 falls through to
  its backoff rule whatever its body quotes. The decision and its
  reason are commented in one place (the field), per the review.
- ToS at 401/403/404 fires exactly as before; the provider-scoped
  deepseek-web ban rule is untouched (its verdicts arrive as 403).

## N5 (MINOR) — internal-proxy token creation is insert-if-absent per key

`getInternalProxyTokens` read the tokens through the 5 s-cached
settings read and persisted generated candidates by blindly overwriting
both keys: two cluster nodes minting concurrently each kept validating
against their own candidate while the row held the last writer's.

- New Repo primitive `FillSettingsRawIfAbsent`
  (`internal/db/settings_writes.go`): merges candidate values only for
  keys the row does not already carry (absent, null, or empty string),
  inside the settings write lock against a forced fresh read, and
  returns the row's effective values. The handler now generates
  candidates only for keys it could not read, persists them through
  this primitive, and adopts the effective values the merge returns —
  a token another node landed first is kept and handed back, so the
  loser of the race validates against the winner's token from that
  call on.
- The residual window is the settings row's whole-blob write itself
  (a merge whose fresh read predates a rival's commit) — the same
  bound every settings writer in this codebase has; the previous
  cache-staleness window (up to 5 s of validating against a replaced
  token) is gone.

## N7 (NIT) — the rotation-stamp snapshot patch mirrors the persisted count

`stampConnection` computed the row's new counter with a relative
increment (`COALESCE(consecutiveUseCount, 0) + 1` under the table
lock), while `patchCachedConnectionStamp` recomputed the snapshot's
count from the snapshot's own stale value + 1 — under cross-node
stamping inside the 1 s TTL the snapshot's ordering value diverged
from the row, contradicting the cycle-1 fragment's freshness claim.

- The stamp UPDATE now ends in `RETURNING consecutiveUseCount` and the
  scanned value — authoritative in both modes (set mode returns the
  literal written; increment mode returns the row-computed count) —
  is what gets patched into the snapshot. A vanished row
  (`sql.ErrNoRows`) commits as a no-op exactly as the pre-RETURNING
  code behaved, and patches nothing.

## N8 (NIT) — FlushUsageWrites ordering and Once discipline

- `FlushUsageWrites` read `usageWriteQueue` directly to decide whether
  a worker existed — an unsynchronized read of a variable the lazy
  `sync.Once` start writes. It now consults `usageWorkerReady`, an
  atomic set inside the Once before the worker starts, so the read
  carries the same happens-before edge the enqueue paths rely on.
- The daily accumulator now flushes AFTER the generic and ledger
  barriers instead of before them: the aggregate summarises the
  ledger, so at a shutdown instant its fold covers only deltas whose
  ledger rows have landed.

## Probe budget (stated BEFORE the runs)

Rig: this tree's binary against scratch databases on the local staging
PostgreSQL 17, threaded mock upstreams (500 ms fixed delay, streaming
chat completions), one seeded connection + API key per database
(`~/tmp-smoke/probe_c2.py`, modelled on the cycle-1 rig).

1. **Load + reconciliation:** 300 requests at 50 workers through the
   gateway; after a drain wait, `usageHistory` and `requestDetails`
   each gain exactly +300 rows; `GET /api/system/memory` reports
   `usageWritesDropped: 0`; zero drop warnings in the gateway log.
2. **N1 proof:** with the gateway up, `DROP TABLE usagehistory` and
   `DROP TABLE requestdetails`, then 20 chats against the live mock.
   Every chat still answers 200 (the failure is off-path). Expected:
   40 ledger records lost (20 history + 20 detail), 40 per-row failure
   log lines, batch-failure log lines reporting the run sizes, and
   `usageWritesDropped` increases by exactly 40. Then restart the
   gateway (the boot schema pass recreates the tables) and fire 5
   chats: both tables gain exactly +5 and the counter does not move.
3. **N3 proof:** `HEAD /v1/models` with the API key → 200 with an
   empty body; `GET /v1/models` unchanged (200, non-empty body); HEAD
   also answers on `/models/{kind}` and the voices spelling.
4. **N4 proof:** mock upstream returning 429 with a "terms of service"
   body → the connection takes the quota backoff path: the stored
   `rateLimitedUntil` is minutes at most, NOT ~24 h. The same body at
   403 → `rateLimitedUntil` ≈ +24 h (banned class intact).
5. **N2 proof:** probe build with `deprecationSetTTL` shortened to 5 s
   (the only source difference from the shipped tree; the shipped
   value is 5 min). Boot, warm up (the set loads, empty), insert a
   deprecation row for the served provider/model directly into the kv
   table ("another node"), fire a chat immediately → the row survives
   (set still stale); after the TTL lapses, fire another chat → the
   clear path sees the row and deletes it.
6. **Static gates:** `go build ./...` and `go vet ./...` green on
   default tags AND with `-tags integration`; `gofmt` clean on every
   touched file.

## Probe results (run 2026-11-05, this tree's binaries, local staging PG)

- **Static gates:** `go build ./...` green; `go vet ./...` green on
  default tags AND with `-tags integration`; `gofmt` clean on all nine
  touched files. Re-run after the N2 probe build was restored: green,
  with `deprecationSetTTL = 5 * time.Minute` in the shipped source.
- **Load + reconciliation (1):** 300 requests at 50 workers, 0 errors,
  p50 549 ms / p95 1,142 ms. Reconciliation EXACT: usageHistory +300,
  requestDetails +300. `usageWritesDropped: 0`; zero drop/failure
  lines in the gateway log.
- **N1 (2):** both ledger tables dropped under the running gateway; 20
  chats all answered 200 (the failure stayed off the response path).
  `usageWritesDropped` moved 0 → **40, exactly the 40 records lost**
  (20 history + 20 detail). The log shows the two batch-failure lines,
  **20 per-row failure lines per table** (the fallback iterated every
  row), and the "rows lost after per-row retry" lines reporting
  20 + 20. After a gateway restart — whose boot schema pass recreated
  both tables empty — 5 chats landed exactly +5/+5 with the counter at
  0: the writer recovers cleanly. (A single-poisoned-row isolation run
  was not staged: the gateway builds the detail payload itself, so no
  client input can poison exactly one row of a batch. The drop-table
  run proves the fallback iterates and counts per row; isolation of a
  good batch-mate follows from the fallback issuing single-record
  statements.)
- **N3 (3):** `HEAD /v1/models` → 200 with an empty body (GET: 200,
  13,270 bytes; neither response sets Content-Length, as before).
  `HEAD /models/task` → 200 = GET, empty body. `HEAD
  /audio/voices?provider=edge-tts` → 200 (GET: 200, 42,463 bytes);
  without a provider, HEAD returns the handler's 400 exactly as GET
  does. `HEAD /v1/models/task` → 200 = GET.
- **N4 (4):** mock 429 with the ToS body → the client saw 429, the
  connection's `backoffLevel` moved to 1 and its `rateLimitedUntil`
  was the seconds-scale quota backoff (≈0 s remaining when read 1.5 s
  later) — NOT a 24 h lock. The same body at 403 →
  `rateLimitedUntil` 86,398 s out (≈24 h): the banned class is intact
  where it belongs.
- **N2 (5):** probe binary built with `deprecationSetTTL` shortened to
  5 s (single-constant difference; shipped source restored and
  re-verified afterwards). After the warmup chat loaded the empty set,
  a deprecation row for openai/gpt-4o-mini was inserted directly into
  kv ("another node"). A chat inside the freshness window left the
  row in place (stale set trusted, as designed); after the TTL
  lapsed, the next chat's clear path reloaded the set, saw the row,
  and deleted it.
- **N5 / N7 / N8** are proven at code level (the merge primitive's
  keep-existing semantics; the RETURNING value feeding the snapshot
  patch; the Once-guarded ready flag and barrier-before-daily order)
  and by the green build/vet on both tag sets; no dedicated live probe
  was staged for them — N5's race needs two processes racing a mint,
  and N7's divergence needs two processes stamping one connection
  inside one second, neither reproducible with the single-gateway rig
  without disproportionate scaffolding.
- Probe databases (`router9_c2fix`, `router9_c2n4`, `router9_c2n2`)
  were dropped and every probe gateway killed by exact PID; the rig
  is preserved at `~/tmp-smoke/probe_c2.py`.
