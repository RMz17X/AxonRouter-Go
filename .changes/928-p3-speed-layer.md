### P3: cluster speed layer, JWT/boot/shutdown readiness, and the PG efficiency pass

Implements §P3 of the upgrade plan plus the owner-ordered Efficiency
Mandate. No provider executor or chat-core restructuring (§3 untouched);
no new dependency — the Redis client is hand-rolled, so `go.mod`/`go.sum`
are unchanged and the §4.F portability conditions are unaffected.

- **New package `internal/speedlayer/`** — a `Store` interface (counters,
  KV with TTL, bounded rings, prefix scans) with three backends:
  `memory.go` (today's in-process state extracted; single-node mode),
  `redis.go` + `resp.go` (Valkey/Redis via `VALKEY_URL`, then `REDIS_URL`;
  a minimal RESP2 client — pool of 8, 2 s dial/command timeouts, AUTH/
  SELECT, 64 MB bulk cap — chosen over the full go-redis graph because
  the layer needs a dozen commands on a 1 vCPU target), and `pg.go` (the
  degradation backend: `speed_kv`/`speed_counters` tables inside the
  application database itself, both dialects). `failover.go` is the
  breaker: 3 consecutive primary failures trip it (one log line), a 5 s
  prober heals it (second log line), and while open every operation is
  served by the database backend — never by per-node memory, which would
  let cluster nodes diverge. Below the threshold a primary error surfaces
  to the caller (callers drop best-effort updates) rather than silently
  splitting writes across two backends. `config.go` does boot selection:
  Redis configured → failover store with an eager Ping deciding whether
  the breaker starts open; an unparseable URL logs loudly and serves from
  the database — never silently from memory; no URL → memory.
- **State moved behind the layer.** The usage tracker's live state
  (`internal/usagetracker/tracker.go`): in-flight counters per model and
  per account×model, the provider/account name entries (10 min TTL), the
  recent-request ring (cap 50, seeded once from DB history when empty),
  and the last-error entry. The proxy-pool rotation cursor
  (`internal/handlers/chat/connections.go`) now advances through the
  shared counter, falling back to the previous in-process atomic when the
  layer errors. The model-lock hot path (`internal/db/speed.go`,
  hooks in `accounts.go`): locks publish to the layer on write and reads
  check it first as a *positive-only* shortcut — a miss, an error, or an
  expired entry always falls through to the existing DB read, so the
  overlay can never change an answer, only make it cheaper. That read
  previously cost one `SELECT data` per candidate connection per request.
- **Deliberately NOT moved, with reasons.** Account-scoped cooldowns and
  rate-limit state stay as plain reads of the connection row the selector
  already loads fresh per request — the DB row is already the shared
  truth, and overlaying it would add a second source. Rotation stamps and
  upstream leases were already DB-serialized (`db/rotation.go`,
  `db/leases.go`) and stay. Session affinity is a hint cache whose
  correctness comes from re-checking the row; the resilience semaphore,
  provider quota caches, and the model-catalog cache are node-local by
  nature. The catalog cache is boot-fetched and rebuildable, so per-node
  copies are safe (noted at the boot site in `internal/app/server.go`;
  sharing it via a volume is a P5 compose choice, not a code change).
- **Boot/status surface.** `InitSpeedLayer` (fx, `internal/app/database.go`)
  builds and installs the store and logs one banner line: node id
  (`NODE_ID` env, else hostname), mode (single/cluster), DB dialect, and
  the speed primary/serving/degraded state. The same state is embedded as
  `speedLayer` in the existing `GET /api/system/memory` payload
  (`internal/handlers/batch7_system.go`) — no new page or endpoint.
  `JWT_SECRET` was already the winning source; the per-node file fallback
  now logs a one-time deprecation warning (`internal/config/config.go`) —
  removing the file path entirely is P4's job, as planned.
- **Graceful shutdown.** The hardcoded 5 s drain is now 30 s by default,
  overridable with `SHUTDOWN_GRACE_SECONDS` (`internal/app/server.go`,
  drain sequence documented at the site). P5's compose files must set
  `stop_grace_period` to match.
- **Efficiency (a) — rebind cache.** `internal/db/exec.go`: rebound SQL is
  cached in a bounded map keyed by the source SQL string (cap 4096,
  reset-and-rewarm past the cap), so steady state never rescans a query
  string to renumber its placeholders. The SQLite path is unchanged.
- **Efficiency (b) — PG pool sizing.** `internal/db/client_pg.go`:
  defaults are now maxOpen **10** / maxIdle **5** (previously a flat 25),
  overridable via `PG_POOL_MAX_OPEN` / `PG_POOL_MAX_IDLE` (invalid values
  fall back, idle clamps to open). Why: the deployment target is 1 vCPU;
  the P1 smokes never queued a checkout at 10; and PostgreSQL's default
  `max_connections=100` means a 4-node cluster at 10/node holds 40% of
  the server with headroom for the migrator and admin sessions, instead
  of one node being able to pin a quarter of it alone.
- **Efficiency (c) — EXPLAIN ANALYZE pass** against the local staging PG
  (router9_migtest, 50,120 usageHistory + 3,080 requestDetails seeded
  rows; router9_test for the small tables, seeded 300 connections /
  10,000 keys for the measurements, cleaned up afterwards). One
  measurable defect found and fixed: **`apiKeys.lookupHash` had no index
  in either schema**, and `FindApiKeyByLookup` is the primary
  authentication path for every hashed key — at 10k keys the lookup was a
  sequential scan at **1.211 ms**; with `idx_ak_lookup` it is an index
  scan at **0.057 ms** and stays flat as the table grows. The index is
  created in both bootstraps *after* the additive-columns pass, because
  `lookupHash` is itself an additive column (the first placement, inside
  the table DDL, failed a fresh-database boot — `no such column` — which
  the fresh-DATA_DIR smoke caught before this shipped). Everything else
  measured healthy and was left alone: usageHistory INSERT 0.046–0.068 ms
  warm; the grouped stats fold 0.15 ms on a 1-day window and 48 ms warm
  over the full 50k range (a whole-window GROUP BY must read every row in
  the window — the per-partition sequential scan is the correct plan, and
  widening the analytics covering index to avoid it would tax every
  insert for a few dashboard reads a minute); requestDetails analytics
  events index-served (3k rows, 14.5 ms incl. JSON extraction); rotation
  `MAX(lastUsedAt)` 0.13 ms at 300 connections; lease takeover/guarded
  insert PK lookups at 0.045–0.066 ms; the speed-layer counter upsert
  (the degradation path's hot op) is the same PK-upsert class.
- **Speed-op timeout, measured.** The hot-path op budget
  (`speedlayer.OpContext`) started at 300 ms and is now **1 s**: on the
  staging rig (Valkey 8.1.10 on agent-x reached through an SSH forward)
  every round trip costs ~300 ms and two-command ops ~620 ms, so a
  300 ms budget timed out every op against a healthy server. A co-located
  Valkey answers in ~1 ms; 1 s keeps a hung backend from stalling a
  request while tolerating tunnelled/cross-site topologies, and the
  breaker still moves traffic to the database after 3 failures.

- **Verification (direct checks only; no test suites run, no test files
  edited — `go vet ./...` compiles the whole tree including tests and is
  clean; `go build ./...` clean; all five `CROSS_TARGETS` from the
  Makefile (linux/darwin/windows, amd64+arm64) build with
  `CGO_ENABLED=0`; gofmt clean on every touched file —
  `internal/app/server.go`, `internal/handlers/dashboard/apikeys_policy.go`
  and four `internal/db/*_test.go` files were already non-gofmt-clean
  before this change, in hunks this change does not touch).** Boot smokes
  on the final binary (`~/tmp-smoke/p3_smoke.py`, fresh DATA_DIR per mode,
  mock upstream, API key created in-script): (1) SQLite single-node —
  banner `mode=single db=sqlite speedPrimary=memory speedServing=memory
  speedDegraded=false`, chat non-stream 200 and stream 200 (SSE + DONE),
  JWT deprecation line present, SIGTERM drain 0.03 s idle; (2) PG
  single-node (router9_test) — banner `db=postgres`, chat 200 both modes;
  (3) `REDIS_URL` unreachable + PG — boot succeeds, banner and
  `/api/system/memory` report `serving=database(degraded), degraded=true`,
  degradation log line present, chat 200 both modes served by the
  database backend; (4) live Valkey (agent-x container via SSH forward) —
  boot banner `speedServing=redis speedDegraded=false`; the gateway's own
  tracker counters were read back out of Valkey under their production
  key shapes (`pending/model/…`, `pending/acct/…`); a per-op protocol
  probe exercised every Store operation against the real server with no
  errors; and under the tunnel's ~300 ms round trips the breaker tripped
  once mid-run (log line), traffic continued on the database backend with
  both chats at 200, and the prober healed it one second later (recovery
  log line) — the full degradation cycle, observed live. A `JWT_SECRET`-set
  boot shows no deprecation line. In modes 2–4 the smoke's connection
  create returns 409 because all three share router9_test and the first
  run's connection persists; the chats use it — noted so the 409 is not
  misread as a failure.
- **Left for later phases, deliberately.** jwt-secret file removal → P4.
  Compose `stop_grace_period` and any shared catalog volume → P5. The
  two-node drill and killing Valkey mid-traffic under load → P8 (the
  single-node trip/heal above is the mechanism proof; the drill is the
  scale proof). Environment note: the local staging PG needed its stale
  `postmaster.pid` removed and `/home/hatch`'s o+x bit restored after the
  last VM restore before its keeper could start it — P8's runbook should
  expect the same.
