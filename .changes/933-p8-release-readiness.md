# 933 — P8: release readiness (§6 cluster drill + load probe)

**Status:** COMPLETE — drills 1–5 and the backup/restore proof executed;
the load probe met its concurrency and error floors but **missed its
latency budget** and found a **usage-accounting shortfall at 2,000-request
scale** (both named in the load-probe section, neither softened).
**Where:** agent-x, scratch copy `/workspaces/9router-go-p8`, compose
project `p8drill` (cluster compose + a drill-only override adding a mock
upstream and a drill client on the stack network). Drill secrets were
generated on agent-x into the scratch `.env` (chmod 600) and are not
recorded here. The stack and scratch dir are torn down at the end of the
phase.

Drill-environment note: the app containers run with
`ALLOW_PRIVATE_NETWORKING=true` from the drill override, because the mock
upstream lives on the compose network's private range and the gateway's
SSRF guard otherwise (correctly) refuses to dial it. The shipped compose
files do not set this.

## Drill 1 — cooldown set through node A honoured by node B: PASS

Setup: one OpenAI connection (FailConn) pointing at a mock that answers
429 with `Retry-After: 900` and a body carrying no model-quota markers,
so the generic-429 path sets the account-level cooldown.

- Chat via node A → HTTP 429 from the gateway; mock recorded exactly 1 hit.
- Node A's dashboard surface: FailConn `rateLimitedUntil =
  2026-11-04T18:34:18Z` (900 s honoured), `backoffLevel = 1`.
- Node B's dashboard surface for the same connection: identical
  `rateLimitedUntil`, in the future.
- GoodConn (healthy mock) created via A; 4 chats via node B → all 200,
  fail-mock hit delta **0**, good-mock hit delta **4**. B routes around
  the cooldown A set.

## Drill 2 — both nodes' active requests visible from either node: PASS

6 concurrent slow streams (mock drips 12 chunks over ~18 s), 3 started
through node A and 3 through node B. Mid-flight, `GET
/api/usage/active-requests`:

- on node A: one group (openrouter / deepseek-chat / SlowConn) **count 6**
- on node B: identical, **count 6**

All 6 streams then completed: 200, 14 chunks, `[DONE]`, ~20 s each.

## Drill 3 — Valkey killed mid-traffic: PASS (with one host-side anomaly)

Continuous LB chat traffic (582 requests over the drill window):

- Baseline status payload (node A): `speedLayer = {mode: cluster,
  primary: redis, serving: redis, degraded: false}`.
- 18:24:17 UTC Valkey stopped mid-traffic. 18:24:19 / 18:24:22 both nodes
  logged `speed layer degraded: redis primary unreachable, serving from
  the database backend`. Status payload during the window: `serving:
  "database(degraded)", degraded: true`.
- Traffic never broke: **582/582 requests status 200** across the kill,
  the degraded window, and the recovery (client-observed p50 217 ms /
  p95 347 ms for the whole run, mock delay 150 ms included).
- Anomaly: at 18:26:28 the restart was attempted, but the stopped Valkey
  container had been **removed** from the host by something outside this
  drill (the `docker compose start` output was lost to an SSH-tunnel cut;
  a later `docker ps -a` showed no valkey container at all). With no
  Valkey process existing, the layer correctly stayed degraded.
  Valkey was recreated empty at 18:28:26 (`up -d valkey`, no persistence
  by design); 18:28:29 node A logged `speed layer recovered: redis
  primary reachable again, resuming primary=redis`; status payload back
  to `serving: redis, degraded: false`.
- No state lost: PG usage rows rose 18 → 603 (**+585**) over the window
  against 582 logged client successes — complete, no shortfall (small
  surplus from adjacent drill traffic at the window edges).

## Drill 4 — one app node killed, LB serves on: PASS (one stall, named)

30 chats through the nginx LB at ~0.7 s cadence; node B's container was
stopped ~14 s in (30 s graceful drain):

- **30/30 chats returned 200** — the surviving node served everything.
- Caveat: the single request straddling the kill stalled ~40 s
  (t=14.8 s → 55.1 s) before completing 200 — the drain window plus
  nginx's 10 s Docker-DNS resolver TTL. No request failed; worst-case
  latency at the failover boundary is the honest cost of the shipped
  30 s drain + resolver settings.

## Load probe — budget (stated before the run)

- **Load:** 2,000 chat requests, **100 concurrent workers**, through the
  nginx LB, model `gemini/gemini-2.5-flash` → mock upstream with a fixed
  500 ms response time.
- **Control:** the same 2,000 / 100 direct to the mock, to separate
  gateway overhead from upstream + host cost.
- **Error budget:** zero non-200 responses (zero gateway-attributable
  5xx tolerated).
- **Latency budget:** gateway p50 ≤ 900 ms, p95 ≤ 1,500 ms, and gateway
  p95 ≤ control p95 + 500 ms.
- **Accounting:** PG `usagehistory` row delta over the closed probe
  window must equal exactly 2,000.
- **Resources:** `docker stats` snapshot of app-1, app-2, nginx,
  postgres, valkey taken during the gateway run.

Results (agent-x is a shared host, load ~17 from other tenants
throughout; run-to-run variance was large, so paired same-window numbers
are the ones to trust):

- **Concurrency floor: MET.** 2,000 requests at 100 concurrent workers
  sustained through the LB, twice (shipped pool defaults and tuned).
- **Error budget: MET at sane settings.** Both 2,000-request runs:
  2,000/2,000 status 200, zero gateway 5xx.
- **Latency budget: MISSED.** Gateway run 1 (shipped defaults): wall
  31.5 s, **p50 1,366 ms, p95 2,837 ms** vs the 900 / 1,500 ms budget.
  Control (same client, same mock, direct): wall 14.0 s, p50 603 ms,
  p95 892 ms. The gateway roughly triples p95 over the raw upstream
  path at 100-way concurrency, and the gateway's own request logs show
  the time inside the app (server-side durations 1.3–2.4 s against a
  500 ms upstream), not in nginx or the client. A concurrency ladder
  (300 requests, pool defaults) localises the inflection: 10 workers
  p50 590 ms / p95 792 ms (clean), 25 workers p95 1,992 ms, 50 workers
  p95 1,804 ms, 100 workers p50 2,990 ms / p95 6,233 ms. Single-digit
  concurrency is unaffected; the cost is a queue that grows with
  concurrency somewhere in the per-request path.
- **Pool tuning is not the lever, and has a trap.** `PG_POOL_MAX_OPEN`
  10 → 32 (with PG `max_connections` raised to match): p50 1,448 ms /
  p95 4,266 ms — no improvement. At 64/node **without** raising PG's
  side, the stack produced the drill's only 5xx: PG refused connections
  (`FATAL: sorry, too many clients already`, SQLSTATE 53300; 64×2
  nodes > PG's default 100), the allowlist read failed closed, one
  request returned 503. The shipped compose files expose **neither**
  knob (the app pool env vars exist in code but are not passed through;
  PG `max_connections` is not set), so an operator who raises one side
  alone walks into the 53300 wall. Config-surface gap, recorded for
  the parent — no code change made in this phase, per P8 rules.
- **Accounting: EXACT at 300 scale, SHORT at 2,000 scale.** Closed-
  window reconciliation: 300 requests / 100 workers → `usagehistory`
  delta +300, `requestdetails` delta +300, exact. The 2,000-request
  tuned run → `usagehistory` delta **+1,899 against 2,000 completed
  200s (101 rows, 5.05%, missing)**; the count was stable on re-read
  45 s later, so it is not flush lag. Arithmetic across the earlier
  high-concurrency runs implies similar shortfalls there too (stated
  as implied, not directly measured). Low-concurrency drills (1–4)
  reconciled fully. Shape is consistent with a bounded async
  usage-write path dropping under sustained burst; the mechanism is
  **not proven** — code-level follow-up for the review cycles, not
  fixed here.
- **Resources during a run** (docker stats, mid-run capture): app
  nodes peaked ~40% of one core and 24–46 MiB RSS each; PostgreSQL
  peaked 36.5% / 126.7 MiB; nginx ≤2.75% / 7.3 MiB; Valkey ≤5.7% /
  9.1 MiB. Nothing is memory-bound; per AGENTS.md §4.G the gateway's
  footprint stays small-box-shaped even while its latency under
  100-way concurrency does not.

## Backup / restore proof (DATABASE.md §8): PASS, doc as written

Executed against the running drill cluster stack, adapting only the
compose `-f`/`-p` flags from the doc's single-stack form to the cluster
file (the doc frames the two stacks as identical in shape):

1. `pg_dump -U 9router -d 9router -Fc -f /tmp/9router.dump` inside the
   postgres container → copied out (317 KB). Pre-dump counts:
   usagehistory 6,268, providerConnections 4, apiKeys 1, settings 1.
2. App stopped; the database was dropped and recreated **empty**
   (stronger than the doc's restore-over-existing path).
3. Dump copied back in; `pg_restore -U 9router -d 9router --clean
   --if-exists /tmp/9router.dump` → no errors.
4. Read-back counts identical: **6,268 / 4 / 1 / 1**. App restarted on
   the restored database: usage stats served the restored history, a
   chat authenticated with the restored API key through a restored
   connection returned 200, status payload healthy in cluster mode.

No DATABASE.md change was needed — the documented sequence works as
written.

## Drill 5 — migrate drill against the cluster stack: PASS

- Stack torn down with volumes; the `migrate` profile from
  **docker-compose.cluster.yml** run against the staged legacy fixture
  (the P5 demo-era DB: 4 keys, 3 connections, 30 usage rows, 37 request
  details, WAL journal). First attempt failed on the agent-x buildx
  scratch-config trap (`BUILDX_CONFIG` — the P5 runbook note); with it
  set, the migrator image built and ran.
- Migrator's own report: every table copied with **0 skipped**
  (settings 1, providerConnections 3, apiKeys 4, usageHistory 30,
  requestDetails 37, usageDaily 1) and its verification line: "every
  table satisfies target = source − skipped"; partition counts in PG
  match the fixture exactly.
- Cluster brought up on the migrated database: both nodes healthy,
  boot banner `mode=cluster … speedServing=redis speedDegraded=false`;
  the dashboard surfaces serve the migrated data — connections list
  shows the fixture's three (OpenAI Mock, OpenRouter Mock, Gemini
  Dead), keys list shows 4, `/api/usage/logs` returns the 30 history
  rows. (`/api/usage/stats` reports totalRequests 32 over the migrated
  set — a distinct-request fold across the history and details tables,
  not a raw row count of either; the PG row counts themselves are
  exact, per the migrator verification above.)

## Makefile rider (P6 flag)

- Removed the stale `mitm-enable` / `mitm-disable` / `mitm-status`
  targets (and their `.PHONY` entries): they invoked the `mitm`
  subcommand deleted with the host CLI surface in P5.
- The `docker` target pointed at the removed `docker-compose.yml`; it
  now runs `docker compose -f docker-compose.single.yml up -d`.
- Verified: `make -n docker` prints the new command; `make -n
  mitm-enable` → "No rule to make target"; no `mitm` references remain
  in the Makefile.
