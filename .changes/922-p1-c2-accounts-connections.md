# P1 cluster C2 — accounts/connections on PostgreSQL

Master plan §5 (P1); survey: `~/workspace/9router-go-P1-worklist.md`
cluster C2 (201 `?` placeholders). Verified against the LOCAL PostgreSQL
17.11 on this VM (127.0.0.1:15432); the SQLite path's behaviour is
unchanged except the two deliberate items named under Decisions. The
step-0 mechanism (`.changes/921`) is reused throughout: statements keep
their `?` text and route through the `Repo` wrappers / `Repo.Rebind`;
dialect branches live only where the SQL itself differs.

## The `json_set` vocabulary (`internal/db/jsonexpr.go`)

C2's Tier A core is SQLite's variadic `json_set` (9 sites in
accounts.go + accounts_oauth_lock.go). jsonexpr gains one rendering per
(dialect, argument kind), composed by `jsonSetExpr` from `jsonSetPair`s:

- SQLite arm: the `json_set(data, …)` call the code has always run.
- PostgreSQL arm: a `jsonb_set` chain — one call per pair, since
  `jsonb_set` takes a single literal path array. Static paths render
  `'{a,b}'`; a bound key renders `ARRAY[?]` and the caller binds the
  bare key (on SQLite the same placeholder binds the full path string —
  `Repo.modelLockKeyArg` picks the argument form per dialect, so a
  model name is data on both arms, never path syntax).
- Values, per kind: a bound string → `to_jsonb(?::text)`, a bound int →
  `to_jsonb(?::bigint)`, a bound JSON document → `?::jsonb` (SQLite
  `json(?)`), JSON null → `'null'::jsonb` for both SQLite spellings
  (`json('null')` and a bare SQL NULL — a SQL NULL argument to
  `jsonb_set` would null the whole document, not the key), a string
  literal → `to_jsonb('…'::text)`, an int literal → `to_jsonb(n::bigint)`.
  The casts are load-bearing, not decoration: an untyped placeholder or
  bare literal leaves the polymorphic `to_jsonb` unresolvable
  (SQLSTATE 42804) — the parity probe caught both forms live before the
  casts landed.

## accounts.go — the restructuring, per call shape

Every statement now routes through the wrappers. The `json_set` sites:

- `LockConnectionModel` — pairs: bound key (quoted-path form) ← string
  param, `backoffLevel` ← int param.
- `RecordConnectionScopedError` — `backoffLevel` ← int, `lastError` ←
  JSON-document param (the nested-object case), `status` ← literal
  `'error'`.
- `LockConnectionRateLimit` — `rateLimitedUntil` ← string, `backoffLevel`
  ← int, `lastError` ← document, `status` ← literal.
- `ClearConnectionRateLimit` — `rateLimitedUntil`/`lastError` ← bare
  SQL NULL pairs, `status` ← literal `'active'`, `backoffLevel` ←
  literal 0.
- `UnlockConnectionModel` — bound key ← `json('null')`, `backoffLevel`
  ← literal 0. This is the explicit-JSON-null case the probes read back.
- `clearSpecificModelLock` — bound key (the historical unquoted `$.…`
  form) ← `json('null')`, `backoffLevel` ← literal 0.
- `clearConnectionModelLocks` — the dynamically built pair list: one
  bound-key `json('null')` pair per `modelLock_*` field found in the
  row, then `backoffLevel` ← literal 0. The builder replaces the
  hand-concatenated `json_set(data, ?, json('null'), …)` string; the
  SQLite rendering is the same call.
- Both clear paths also had the file's two `datetime('now')` stamps;
  see Decisions.

`accounts_oauth_lock.go` — the same vocabulary, two sites:
`RecordConnectionOAuthFailure` (string/int/document params) and
`ClearConnectionOAuthLock` (bare-NULL + literal-0 pairs).

## The two boot warnings — fixed

- `provider_id_migrate.go` — `MigrateLegacyProviderIDs` takes a raw
  `*sql.DB`, so its UPDATE now passes through
  `rebindQuery(DialectOf(db), …)`. The `?` placeholders were the whole
  bug: PostgreSQL parses `?` as an operator, hence "syntax error at or
  near WHERE".
- `thought_signatures.go` — all statements through the wrappers
  (placeholders were the "syntax error at or near ORDER"), plus the
  two constructs PostgreSQL lacks: the newest-first order's tiebreak
  is `rowid` on SQLite and the primary key on PostgreSQL
  (`thoughtSignatureOrder` — it only arbitrates rows written inside
  the same second), and the trim's unbounded window is `LIMIT -1` on
  SQLite, `LIMIT ALL` on PostgreSQL, which rejects a negative limit.

## The mechanical remainder (placeholders only)

`connections.go`, `batch7_connections_import.go`, `proxyPools.go`,
`proxyGroups.go`, `proxyGroups_sync.go`, `provider_limits.go`,
`provider_quota.go` (tx statements via `r.Rebind`), `providernodes.go`
(same for `DeleteProviderNode`; `pruneCustomModels` now takes the
Repo's `Rebind` as a parameter, as do the rename helpers),
`leases.go`, and `vault.go` (below).

- `provider_node_rename.go` — every tx statement rebound; the
  `sqlite_master` table-exists probe is dialect-branched: SQLite keeps
  its `sqlite_master` count, PostgreSQL uses
  `SELECT to_regclass($1) IS NOT NULL` — the `tableExistsOnDB` form
  from C1, with `$1` spelled literally inside its own branch.
- `leases.go` — beyond placeholders: `isLeaseConflict` also matches
  PostgreSQL's duplicate-key report (SQLSTATE 23505 text). Without it,
  losing the INSERT…WHERE NOT EXISTS race on PostgreSQL surfaced as an
  error instead of "not acquired". `EnsureUpstreamLeases`' DDL has no
  placeholders and is valid in both dialects; it stays on the raw
  handle like the other boot-time ensure helpers.
- `rotation.go` — the worklist called this file mechanical; it is
  not: `BEGIN IMMEDIATE` is a syntax error on PostgreSQL. The stamp
  transaction now branches: SQLite keeps `BEGIN IMMEDIATE`;
  PostgreSQL runs a plain `BEGIN` plus
  `LOCK TABLE providerConnections IN EXCLUSIVE MODE`, whose lock
  conflicts with the row-exclusive lock every UPDATE takes — the same
  writer-serializing reach, held for the same span. The MAX read and
  the UPDATE on the dedicated connection rebind via `r.Rebind`.
- Two C2-path dependencies outside the worklist's file list, pulled in
  because C2's own methods call them (leaving them unported would
  leave proven-broken statements inside the C2 surface):
  `dashboard.go`'s `UpdateConnectionData` only (one statement;
  accounts.go's `ClearConnectionModelLocks` and proxyGroups' reference
  clearing write through it — the rest of dashboard.go stays C3), and
  `vault.go`'s six statements (the worklist's §6 lists vault.go as
  bind-free; its dynamic-column seal statements do bind — the seal
  round-trip is part of every connection write when a vault is on).

## Decisions recorded

- **`datetime('now')` → `nowUTC()` on both dialects** (the worklist's
  instruction for these two sites). This is the one deliberate
  SQLite-visible change in the cluster: the two clear paths now stamp
  `updatedAt` with the file-standard RFC3339 text instead of SQLite's
  `YYYY-MM-DD HH:MM:SS` form — the same instant, in the format every
  other writer in the file already uses (and the only format
  PostgreSQL was ever going to store). It is visible in exactly one
  dashboard field; the probes pin it below.
- **Dotted model names in the two clear paths: a pre-existing SQLite
  quirk, preserved, not reproduced.** The clear paths bind the lock
  key as an *unquoted* `$.modelLock_<model>` path, so for a model with
  a dot (`gpt-4.1-mini`) SQLite's json_set parses a nested path and
  the clear also writes a phantom `"modelLock_gpt-4": {"1-mini": null}`
  object. HEAD does this too (probe evidence below); the SQLite arm
  keeps the historical argument byte-for-byte, quirks included. The
  PostgreSQL arm binds the bare key into `ARRAY[?]`, which cannot
  express the misparse — it nulls exactly the one key. Lock/unlock
  were never affected (they use the quoted `modelLockDataKey` form).
  Fixing the SQLite parse is a behaviour change for a later,
  deliberate patch, not a dialect sweep's business.

## Verification (direct checks only; no test suites run)

- **Build gate:** `go build ./...` and `go vet ./...` green; gofmt
  clean on all 18 touched files (the scratch probe harnesses were
  deleted before the gate, so the tree that was gated is the tree
  being committed).
- **PostgreSQL boot smoke** (fresh database, local PG 17.11): the
  HEAD binary's boot log carries both warnings —

  ```
  WRN [db] legacy provider id migration failed error=migrate legacy provider id "ollama-cloud" -> "ollama": ERROR: syntax error at or near "WHERE" (SQLSTATE 42601)
  WRN [translator] thought signature hydration skipped error=load thought signatures: ERROR: syntax error at or near "ORDER" (SQLSTATE 42601)
  ```

  The post-change binary's boot log (two boots) contains **zero**
  warn/error lines; `/api/auth/status` answers 200.
- **Repo-level parity probes** — one fixed script (~90 operations:
  connection CRUD + imported connections, the full lock/error/
  rate-limit/OAuth-lock state machine incl. both clear paths, node
  create/rename/delete, limits + quota reset/usage, pools, groups,
  leases, rotation, thought signatures, the seal round-trip,
  `UpdateConnectionData`), run on SQLite-HEAD, SQLite-post-change and
  PostgreSQL, read-backs compared as canonical JSON documents:
  - PostgreSQL vs SQLite-post-change: **identical**, except the
    phantom-key divergence above (PostgreSQL correct, SQLite legacy).
  - SQLite post-change vs HEAD: **identical**, except the two
    `updatedAt` format booleans from the `datetime('now')` decision.
  - The `json('null')` read-back is explicit in the dump: after
    `UnlockConnectionModel`, `modelLock_gpt-4o` is present with value
    `null` and `backoffLevel` is `0` on all three backends; the nested
    `lastError` document round-trips through the document-param pair.
  - Raw SQLite text, post-change vs HEAD: identical modulo wall-clock
    values and the `lastError` payload's key order — which is
    process-level nondeterminism in the pre-existing Go map marshal,
    demonstrated on HEAD itself (two HEAD runs emit different orders;
    four runs of each build span the same set of orders).
- **Lease behaviour under concurrency** (the worklist's C2.3
  requirement): 8 goroutines race `AcquireLease` on one key — exactly
  **1 winner on PostgreSQL and 1 on SQLite**, and the stored holder is
  the winner on both. Sequential contention, refresh-by-value,
  release-by-value and expired-takeover all match across dialects in
  the main probe.
- **HTTP parity probes** — identical seeds (the probe script) behind
  the real binaries; `GET /api/auth/status`, `/api/connections`,
  `/api/provider-nodes`, `/api/proxy-pools`, `/api/proxy-groups` all
  200 on PostgreSQL, SQLite-post-change and SQLite-HEAD, bodies
  identical after timestamp normalization — except conn2's
  `updatedAt` in the HEAD run reading `2026-11-04 13:07:40` (the
  legacy `datetime('now')` text) where both post-change backends read
  RFC3339: the Decisions item, surfacing in its one field.

## Deliberately left for later clusters (named, untouched)

- **C3** — the rest of `dashboard.go` (incl. `UpdateProviderConnection`,
  `DeleteProviderConnection`, `SetConnectionStatus`,
  `ReorderProviderConnections`), so `PUT`/`DELETE /api/connections/{id}`
  still fail on PostgreSQL until C3; `apikeys.go` (the end-to-end PG
  chat stays C3-gated, as C1 recorded); plans/modelaccess probes.
- **C4** — `handlers/chat/gemini_handler.go`'s one `json_set` site
  (same vocabulary now exists for it in jsonexpr.go).
