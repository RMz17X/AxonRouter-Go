### P2 — one-shot SQLite → PostgreSQL data migrator (`tools/migrate-db`)

- New standalone tool at `tools/migrate-db/` with its **own module**
  (`module migrate-db`; requires only `pgx/v5` + `modernc.org/sqlite`,
  the same versions the runtime pins). It imports none of the app's
  packages and reads both schemas as data; the main module's
  `go.mod`/`go.sum` are untouched (verified by diff) and the runtime
  never links it. Usage:
  `migrate-db --sqlite <data.sqlite> --pg <DATABASE_URL> [--truncate]`.
- **Schema**: the tool applies the same DDL the app applies on a PG
  boot, transcribed as data into the embedded `schema_pg.sql` (from
  `internal/db/schema_pg_defs.go`, the partitioned parents in
  `schema_pg.go`, `thought_signatures.go`, and the lazily created
  `upstream_leases` from `leases.go`). `pgGoOnlyColumns` are applied as
  a second `ADD COLUMN IF NOT EXISTS` section, mirroring the app's
  create-then-additive end state; seed rows are applied **after** the
  copy with the app's own `ON CONFLICT DO NOTHING` statements.
  Equivalence is proven, not asserted: the column catalog
  (information_schema, 212 table.column entries) of a migrator-created
  database is **byte-identical** to that of an empty database the app
  booted itself.
- **Non-empty-target rule (exact)**: after schema creation the tool
  counts every table it would write; if any holds rows and
  `--truncate` was not given it refuses (exit 1, naming each non-empty
  table and its count) and copies nothing. With `--truncate` it empties
  those tables first and rebuilds. Idempotency contract: a re-run
  without `--truncate` refuses and leaves the migrated state untouched;
  a `--truncate` re-run reproduces the identical end state.
- **Copy**: every source table with a target is copied in dependency
  order — `_meta, settings, plans, providerNodes, providerConnections,
  proxyPools, proxyGroups, apiKeys, api_key_model_access, combos, kv,
  guardrail_policies, guardrail_logs, usageDaily,
  usageLedgerCarryover, providerLedgerCarryover, thoughtSignatures,
  upstream_leases, semanticCacheEntries, files, batches,
  compressionAnalytics, mcp_tool_audit, usageHistory, requestDetails`
  (the schema declares no foreign keys; unlisted source tables with a
  target follow alphabetically). Bulk path is **pgx `CopyFrom`** in
  5,000-row chunks for every table except `mcp_tool_audit`, whose
  `GENERATED ALWAYS` identity takes batched
  `INSERT … OVERRIDING SYSTEM VALUE` (COPY cannot express the
  override). Ids and timestamps are preserved exactly; identity
  sequences are advanced past the greatest copied id afterwards.
  Measured throughput on the fixture (local PG 17.11, this VM):
  **usageHistory 50,120 rows at ~15,000 rows/sec**, requestDetails
  3,080 rows at ~20,800 rows/sec (fresh-target run; the
  truncate-rebuild run measured ~11,100 / ~8,100 rows/sec).
- **Type conversion** follows the schema port: the ten jsonb payload
  columns (`settings.data`, `providerConnections.data`,
  `providerNodes.data`, `proxyPools.data`, `proxyGroups.data`,
  `combos.models`, `usageDaily.data`, `usageHistory.tokens`,
  `usageHistory.meta`, `requestDetails.data`) are JSON-validated per
  row; text-contract columns (`kv.value`, `apiKeys.metadata`, …) copy
  as text unvalidated, exactly as the port's type contract prescribes.
- **Malformed-JSON policy: skip-and-report, never fatal, never
  mangled** (the plan's P2 rule). A row whose jsonb payload fails
  validation — or whose value has no faithful target representation —
  is skipped; the report names the table, the row's first-column
  identity, the column, and the reason. The closing verification pass
  requires `target = source − skipped` per table, compares min/max
  timestamps on the two hot tables, and sums the per-partition counts
  against the parent; any other mismatch exits 1. A known table
  (copyOrder) holding source rows but having no target is likewise a
  verification failure; a genuinely foreign table stays report-only.
- **Partitions**: the tool creates partitions for the current + next
  month (the app's own guarantee) plus **every month present in the
  source hot tables** — there is no DEFAULT partition, by the app's
  design, so a source month without a partition would fail loudly
  instead of being filed silently.
- Verification (direct checks only; fixture = app-booted SQLite file
  seeded via API — key, connection, combo — plus direct writes:
  50,120 usageHistory rows across 2026-09/10/11, 3,080 requestDetails,
  and rows in 11 further tables):
  - Per-table counts match the source exactly on a fresh target;
    partition routing proven per partition (usagehistory
    y2026m09=49,847, y2026m10=153, y2026m11=120; requestdetails
    y2026m09=3,000, y2026m11=80); min/max timestamps verified.
  - Read-backs: the migrated connection's `data` and one usage row's
    `tokens` are JSON-deep-equal to the SQLite originals; `createdAt`
    and the usage timestamp are byte-equal; the usageHistory identity
    sequence sits ahead of the data (max 50,120, next 50,121).
  - Re-run without `--truncate` → exit 1 refusal, data untouched;
    with `--truncate` → exit 0, identical counts.
  - Malformed run (one `providerConnections.data`, one
    `usageHistory.tokens` corrupted): exit 0, exactly those 2 rows
    skipped with reasons, verification passed on `source − skipped`.
  - App booted against the migrated database: zero dialect lines in
    the boot log, `/api/auth/status` 200, `/api/connections` lists the
    migrated connection, `/api/usage/request-details` returns migrated
    rows, and `/api/usage/history` aggregates are **identical** to the
    same binary reading the source SQLite file (120 requests,
    68,662 prompt + 33,029 completion tokens in the endpoint's default
    window). `/api/usage/stats` defaults to a "today" window the
    fixture predates and returns zeros on **both** backends — recorded
    here so the zero is never misread as a migration loss.
  - Two defects were caught by these checks and fixed during the
    build: the first run's catalog query missed the partitioned
    parents (relkind `p`, not `r`), which is what motivated the strict
    no-target failure above; and the first migrated-boot probes hit a
    leftover P1 smoke server still listening on :20130 — the boot
    proof was redone on a dedicated port with the listening process
    verified to be the freshly booted binary.
- Out of scope by design: the `9router-migrate` image / compose
  `migrate` profile (P5 wires this binary into Docker delivery), and
  any runtime-side efficiency work (later phase). The throwaway
  verification databases were dropped except `router9_migtest`, kept
  on the local staging server for inspection.
