# P1 step 0 + cluster C1 — placeholder mechanism; usage cluster on PostgreSQL

Master plan §5 (P1) and §8 acceptance recipe; survey:
`~/workspace/9router-go-P1-worklist.md` cluster C1. Everything below ran
against the LOCAL PostgreSQL 17.11 on this VM (127.0.0.1:15432,
`router9_test`), which supersedes the worklist's agent-x precondition.
The SQLite path is byte-identical after the sweep (probe diff below);
the cutover stays a later phase.

## Step 0 — the placeholder mechanism (`internal/db/exec.go`)

Two new files; no call-site SQL text changes form.

- `rebindQuery(d, q)` rewrites `?` placeholders to `$1..$n` when the
  dialect is PostgreSQL and is the identity for SQLite. The scan is
  quote- and comment-aware (single/double/backtick quoting, `--` and
  `/* */` comments), so a `?` inside a string literal or a comment is
  never renumbered. PostgreSQL-native SQL (schema_pg.go) and
  SQLite-only paths keep the raw handle.
- `Repo` gains `Exec/Query/QueryRow` and `ExecContext/QueryContext/
  QueryRowContext` wrappers that rebind through the repo's own dialect
  and delegate to the handle. SQL text therefore stays in the `?` form
  the codebase already writes; the dialect exists only at execution.
- `Repo.Rebind(q)` covers the two shapes the wrappers cannot: a
  statement prepared inside a `*sql.Tx` (the ledger chunk writer), and
  a statement built by a helper holding a raw `*sql.DB`.

**Adoption pattern for later clusters (C2–C4):** route the file's
statements through the wrappers; keep every statement's text in `?`
form; branch only where the SQL itself differs (a JSON extraction, an
upsert form, a SQLite-only construct) using the `jsonexpr.go` helpers
and `r.IsPostgres()` / `r.dialect`; never write `$n` in a shared
statement. One-off PostgreSQL-only probes on a raw handle (there is
exactly one: `tableExistsOnDB`) spell `$1` literally inside their
PostgreSQL branch.

## The JSON vocabulary (`internal/db/jsonexpr.go`)

One rendering per (dialect, operation), so the ~98 in-cluster JSON
sites stay uniform:

- Path builder: SQLite `'$.a.b'` / `'$.a[0].b'`; PostgreSQL `'{a,b}'` /
  `'{a,0,b}'` (an all-digits segment is an array index in both).
- `jsonTextAt` (`#>>`), `jsonNumAt` (`::numeric`), `jsonIntAt`
  (`trunc(text::numeric)::bigint` — mirrors SQLite's
  `CAST(x AS INTEGER)`, which truncates toward zero; a plain `::bigint`
  cast would round), `jsonRealAt` (`::float8`), `jsonDocAt` (`#>`; the
  document comes back in jsonb's canonical rendering — same document,
  possibly different key order than the stored text), `jsonValid`
  (PostgreSQL: `column IS NOT NULL` — jsonb is valid by construction,
  and a JSON `null` and a missing key both read as SQL NULL there,
  which is the same value the SQLite guard produces for them).
- The SQLite arm of every helper returns the extraction the code has
  always run, so a ported statement's SQLite text is unchanged except
  where a guard was restructured (named per file below).

## Per-file changes

- `internal/db/usage.go` — all statements through the wrappers.
  `UpsertUsageDaily` branches to `ON CONFLICT (dateKey) DO UPDATE`
  (SQLite keeps `INSERT OR REPLACE`, which is delete-then-insert; the
  PostgreSQL form updates in place — same final row, the only
  statement whose SQLite *form* has no twin). `InsertRequestDetail`
  branches to untargeted `ON CONFLICT DO NOTHING` (grain note below).
  The request-detail SELECT twins (`requestDetailPerfSelectPG`,
  `requestDetailListSelectPG`) render literally — trunc-form token
  extractions, `data #> '{tokens}'` with a `'{}'::jsonb` default, and
  the validity column as `CASE WHEN data IS NULL THEN 0 ELSE 1 END`
  (jsonb needs no json_valid probe). `GetComboAnalyticsSince` branches
  its combo extraction and latency average. New `cacheHitTokenExpr`
  renders the cache-hit CTE's guarded extractions per dialect (SQLite
  text identical to the previous inline form).
- `internal/db/usage_analytics.go` — expression selectors
  (`usageTotalTokensExprFor`, `failureSampleErrorExprFor`,
  `failureSampleStatusExprFor`) return the existing SQLite constants
  verbatim on that arm and compose the PostgreSQL twin from
  `jsonexpr.go` (the SQLite guards collapse: jsonb absence is already
  NULL; the status/error NULLIF chains run over `#>>`).
  `GetAnalyticsEvents` takes its latency/token/category columns per
  dialect. `GetRecentFailurePayloads` reads `COALESCE(data::text, '')`
  on PostgreSQL. `GetRecentUsageLogRows` reads
  `COALESCE(meta::text, '')` on PostgreSQL — the bare form asks the
  server to parse `''` as JSON (SQLSTATE 22P02; this was the one live
  bug the parity probes caught, /api/usage/logs 500).
- `internal/db/cache_analytics.go` — `sqlCachedTokensExprFor` /
  `sqlCacheCreationTokensExprFor` (the creation/cached sub-expressions
  are composed once and shared by the CASE arms, mirroring the SQLite
  shape). New `hourBucketExpr(d)`, shared with compression: SQLite
  keeps `strftime('%Y-%m-%dT%H:00:00Z', timestamp)`; PostgreSQL renders
  `substring(timestamp, 1, 13) || ':00:00Z'` — timestamps are stored as
  zero-padded UTC RFC3339 text in both dialects, so the hour bucket is
  a prefix, with no timezone or format conversion to get wrong.
- `internal/db/compression_analytics.go` — all reads through the
  wrappers; `countGroups`/`droppedGroups` now take the `*Repo` (their
  uncapped-count statement needs the rebind like any other). The
  table-path trend uses `hourBucketExpr`. The usageHistory-backfill
  query set is built from five named extraction locals (coalesced and
  bare forms) per dialect; the `json_valid` guard exists only on the
  SQLite arm. `BackfillCompressionAnalytics` gains a PostgreSQL
  early-return: it is SQLite-only by construction (the PostgreSQL
  schema deliberately omits it — a repair of SQLite-era rows, and the
  data migrator owns repairs for imported ones); the guard states that
  contract at the function instead of letting a future caller run
  SQLite dialect halfway on a PostgreSQL handle.
- `internal/db/usage_retention.go` — counts through the wrappers. The
  requestDetails chunk deletes name their rows by identity: `rowid`
  on SQLite, the `(id, timestamp)` partition key on PostgreSQL.
- `internal/db/usage_retention_ledger.go` — the ledger row's identity
  is its `id` (on SQLite `id` IS the rowid alias — the schema declares
  it `INTEGER PRIMARY KEY` — so the cursor, the quota-reset marks and
  the chunk delete keep one name in both dialects; the pre-sweep code
  selected `rowid`, which PostgreSQL does not have). Transaction
  statements rebind via `r.Rebind`. The chunk delete expands its JSON
  id set with `json_each` (SQLite) or `json_array_elements_text(?::json)`
  (PostgreSQL). The carryover upserts' accumulation names its target
  table on PostgreSQL (`tokens = usageLedgerCarryover.tokens +
  excluded.tokens`): the bare column is ambiguous with the EXCLUDED
  row's same-named column there (SQLSTATE 42702 — the second live bug
  the checks caught; the SQLite arm is unchanged).
- `internal/db/usage_retention_loop.go`,
  `internal/db/usage_retention.go` (stats/vacuum) — the retention
  PRAGMA decision, below.
- `internal/db/schema.go` — `tableExistsOnDB` gains a PostgreSQL arm
  (`SELECT to_regclass($1) IS NOT NULL`). It is a runtime probe too,
  not only bootstrap: the ledger prune asks it about `providerNodes`
  on any backend, and `sqlite_master` does not exist on PostgreSQL.

## Decisions recorded

- **Retention PRAGMAs are dialect-gated no-ops on PostgreSQL.**
  `GetDatabaseStats` reads `page_count`/`page_size`/`freelist_count`
  only on SQLite; on PostgreSQL `databaseSizeBytes` and
  `reclaimableBytes` report 0 — deliberately NOT a `pg_database_size`
  transliteration, which would answer a different question (the whole
  shared database) under a field that means "this gateway's file".
  The row counts and ledger bounds are dialect-neutral and read on
  both. `VacuumDatabase` returns nil on PostgreSQL (autovacuum owns
  reclaiming; the only storage-returning form, VACUUM FULL, is a
  locking operation no maintenance button should fire). Both
  `PRAGMA optimize` sites (ledger prune tail, retention loop tail)
  are skipped on PostgreSQL — autoanalyze owns the same job
  server-side. No replacement semantics were invented for any of these.
- **`InsertRequestDetail` conflict grain.** SQLite's id-only primary
  key skips a duplicate id at any timestamp; the partitioned
  PostgreSQL parent's key is `(id, timestamp)`, and a partitioned
  table cannot carry a unique index on id alone, so the skip fires on
  the full key. The writers generate one id per request and insert it
  once (the chat path's success/failure branches are exclusive), so
  the wider grain is unreachable from the current call graph; a future
  caller that retries an insert must dedupe itself. Verified live:
  same id at two timestamps stores two rows on PostgreSQL, one on
  SQLite (by design of the two schemas, not a defect of the port).
- **JSON null parity.** A stored JSON `null` and a missing key are
  indistinguishable through every helper (both read as SQL NULL in
  both dialects), which matches what the SQLite extractions produced.

## Deliberately left for later clusters (named, untouched)

- **C2** — the two known PostgreSQL boot warnings live in C2 files and
  still appear, unchanged, after this sweep:
  `internal/db/provider_id_migrate.go` (legacy provider-id migration,
  "syntax error at or near WHERE") and
  `internal/db/thought_signatures.go` `LoadThoughtSignatures`
  (thought-signature hydration, "syntax error at or near ORDER" —
  `ORDER BY … rowid`, `LIMIT -1`). Both are outside C1's file set.
- **C3** — the client-key surface gates the end-to-end PostgreSQL chat
  check: `internal/db/apikeys.go` `queryApiKey` still runs unrebound
  (`get api key: ERROR: syntax error at or near "LIMIT"`), and
  `POST /api/keys` fails on PostgreSQL for the same reason. Both are
  apikeys-cluster work; C1's write statements were proven at the Repo
  level instead (below).

## Verification (direct checks only; no test suites run)

- **Build gate:** `go build ./...` and `go vet ./...` green;
  gofmt clean on every touched file. No new dependencies (the two new
  files are stdlib-only), so the §4.F cross-target gate is unaffected.
- **Parity probes** — the same 14 GETs (usage stats 30d + 24h, chart,
  analytics + failures, history, logs, request-details list + one
  detail, combos analytics, cache, compression, database stats)
  against identical seeds (5 usageHistory, 5 requestDetails,
  3 compressionAnalytics, 1 usageDaily) on both backends:
  - SQLite, post-change vs the pre-change HEAD binary on the same
    seed: **identical**, leaf-for-leaf, except the stats payload's
    `last10Minutes[].timestamp` wall-clock buckets.
  - PostgreSQL vs the SQLite baseline: **identical** except (a) the
    two database-stats size fields = 0 (the decision above) and
    (b) `/api/usage/analytics/failures` `topProviders` order: both
    providers have exactly 1 failure and the pre-existing query orders
    by count only, so the tie breaks by engine plan — same rows, same
    values, different sequence. Pre-change, the same PostgreSQL boot
    returned 500 on 7 of the 14 probes.
  - PostgreSQL boot log after the sweep contains no C1-owned dialect
    errors; the only two warnings are the C2 pair named above.
- **Write path (§8).** SQLite: real chats (stream + non-stream)
  through a mock upstream — 200/200, usageHistory +2 rows with
  `total_tokens` 133 (111+22), requestDetails +2. PostgreSQL: the
  HTTP chat is C3-gated (above), so the same statements ran at the
  Repo level — `InsertUsageHistory` (row lands, tokens jsonb intact),
  `InsertRequestDetail` ×2, `UpsertUsageDaily` insert +
  conflict-update (one row, second payload wins) — all clean.
- **Retention, mirrored on both dialects** (scratch harness, deleted
  after the run; aged rows in an August partition on PostgreSQL):
  `CountExpiredUsageHistory` = 2 rows on both; `PruneUsageHistory`
  removed 2 and carried exactly 300 tokens (2 × (100+50)) into
  `usageLedgerCarryover` on both, fresh row surviving, second pass a
  no-op; requestDetails age prune removed 3 of 5 and size prune
  removed 3 of 5 keeping the newest two on both; `GetDatabaseStats`
  counts/bounds agree (sizes real on SQLite, 0 on PostgreSQL);
  `VacuumDatabase` ok on both.
- **SQLite guard regression:** a usageHistory row with `tokens =
  'not-json'` (impossible on PostgreSQL — jsonb rejects it at write)
  keeps /api/cache, /api/usage/stats and /api/analytics/compression at
  200 on both the HEAD binary and the post-change binary — the
  json_valid guard survived the port.
