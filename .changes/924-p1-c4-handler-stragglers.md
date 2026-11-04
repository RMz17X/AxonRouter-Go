### P1 cluster C4 — handler stragglers: the dialect sweep is complete

Master plan §5 (P1); survey: `~/workspace/9router-go-P1-worklist.md`
cluster C4 (267 `?` placeholders outside `internal/db`). Verified against
the LOCAL PostgreSQL 17.11 on this VM (127.0.0.1:15432, `router9_test`);
the SQLite path is byte-identical after the sweep (probe diffs below).
The step-0 mechanism (`.changes/921`) is reused throughout: statements
keep their `?` text and route through the `Repo` wrappers / `Repo.Rebind`;
dialect branches live only where the SQL itself differs. **With this
cluster, every runtime SQL site in the tree is dialect-ported** — the
closure census at the bottom names everything that remains and why.

#### settings.go — the credentials import (8 × INSERT OR REPLACE)

`importDatabase` runs its inserts on a transaction taken from
`RawDB()`; they now execute through a local `importExec` helper
(`tx.Exec` + `Repo.Rebind`) and pick their upsert form through
`importUpsert(sqliteForm, pgForm)`. `writeKVPayload` became a handler
method to reach them. The settings upsert (already `ON CONFLICT` form)
only gained the rebind.

The REPLACE decisions, per table. Two facts frame all of them, both
read off the schema before deciding: **the schema declares no foreign
keys at all** (`internal/db/client.go`: "declares no FOREIGN KEY
constraints today"; neither DDL set contains a REFERENCES clause), so
REPLACE's delete fires no cascades on either backend; and the import
**wipes every table inside the same transaction before inserting**, so
a conflict can only be with a row this same import wrote moments
earlier through the identical column list — unlisted add-on columns
hold defaults under both forms, and the final row is the same.

- `providerConnections` — `ON CONFLICT (id) DO UPDATE`, every non-key
  column set from EXCLUDED. Sole unique index is the PK.
- `providerNodes` — `ON CONFLICT (id) DO UPDATE`, all 12 non-key
  columns. Sole unique index is the PK.
- `proxyPools` — `ON CONFLICT (id) DO UPDATE`. Sole unique index is
  the PK.
- `apiKeys` — **delete-then-insert emulation**: `DELETE FROM apiKeys
  WHERE id = ? OR key = ?`, then a plain INSERT. apiKeys carries a
  second unique index (`key` UNIQUE), and REPLACE resolves a conflict
  on *either* index by deleting the conflicting row; a single
  `ON CONFLICT` target cannot express that — with target `(id)`, a
  payload whose second key row reuses the first row's key under a new
  id would fail the whole import on PostgreSQL where SQLite succeeds.
  The probe payload exercises exactly that case (plus the duplicate-id
  case) and the PostgreSQL table state matches SQLite byte for byte,
  including the first key's orphaned `api_key_model_access` rows —
  children are *not* cascade-deleted on SQLite (no FK), so the
  emulation must not delete them either, and does not.
- `api_key_model_access` — `ON CONFLICT (api_key_id, model) DO UPDATE
  SET created_at = EXCLUDED.created_at`. The insert names every column
  of the table, so update-in-place and replace are the same row.
- `combos` — **delete-then-insert emulation**, same reason as apiKeys:
  second unique index `name` UNIQUE (`DELETE … WHERE id = ? OR
  name = ?`). The probe's duplicate-name pair lands identically.
- `kv` (both `writeKVPayload` branches) — `ON CONFLICT (scope, key)
  DO UPDATE SET value = EXCLUDED.value`; the insert names every column.

One adjacent defect the import-parity probe caught and this cluster
fixes: **the export read-back was lossy on PostgreSQL.**
`exportDatabase` builds row maps keyed by driver column names and looks
canonical camelCase names up in them (`mergeDataColumn`'s keep list,
`normalizeBool`); PostgreSQL folds unquoted identifiers to lowercase,
so `authType`, `createdAt`, `isActive`, … missed and silently dropped
out of the backup (and `isActive` stayed a number). `selectRows` — the
one place every export read flows through — now restores the schema's
spelling via a canonical-name map; names absent from the map pass
through unchanged, and SQLite (which already returns the DDL case) is
unaffected. Without this, "import → read-back identical" cannot hold
on PostgreSQL no matter how the import itself is ported.

#### handlers/oauth — the connection-upsert family (17 files)

One repeated shape (read the stored blob, merge, UPDATE — else INSERT
into providerConnections), spelled per provider in antigravity.go,
authcode.go, batch3_bulkjwt.go, batch3_dynamic2.go, batch3_mimo.go,
batch3_shared.go, cline.go, device.go, freebuff.go, grokcli_bulk.go,
kiro_import.go, oauth.go, pkce.go, special.go, trae.go, windsurf.go,
zed.go, plus `zcodeExistingIdentity` (zcode_device.go). No Tier A
markers anywhere in the family; the port is the uniform receiver swap —
`h.Repo.RawDB().Exec/Query/QueryRow` → the `Repo` wrappers of the same
name (batch3_shared's `db :=` local now binds the Repo),
`zcodeExistingIdentity` takes the Repo instead of the raw handle. No
shared helper was introduced: the column lists genuinely differ per
provider (email on the shared helper, priority on the device lane),
the shared execution layer *is* the step-0 wrapper mechanism, and the
owner's boundary leaves the provider flows' shapes alone. The nil
guards (`h.Repo.RawDB() != nil`) are unchanged.

Deliberately untouched in the same files — **foreign SQLite files**,
other applications' own databases read read-only, correct as SQLite
forever: special.go's credential-store read (`itemTable`),
batch3_zed_keyring.go (`kv_store` in Zed's keyring DB),
batch3_mimo_desktop.go (the Xiaomi desktop cookie DB copy). Together
with `internal/browsercookies/` (Chromium's cookie DB — no Tier A
sites found there; its two `?` reads stay) these are the same class
the survey excluded for browsercookies, named here so the closure
census is complete.

#### handlers/chat/gemini_handler.go — the last json_set

The `json_set(data, '$.projectId', ?)` persistence moved into the db
package as `Repo.SetConnectionProjectID`, rendered by the C2 jsonexpr
vocabulary (`jsonSetPairAt(jsonStringParam(), "projectId")` — SQLite
keeps the exact statement text; PostgreSQL gets
`jsonb_set(data, '{projectId}', to_jsonb($1::text))`). The statement
lives with the other connection-data updaters in accounts.go because
the vocabulary is unexported there. The file's five remaining raw
handles (`db := h.Repo.RawDB()` feeding QueryRowContext/ExecContext
token-refresh reads and writes) now bind the Repo the same way.

#### semanticcache/persistent_store.go + guardrails/resolver.go

Both stores hold an injected `*sql.DB` and were blind to dialect. Each
gains the rewrite as a plain function value, keeping both packages
independent of the repository layer:

- semanticcache: `NewPersistentStoreDialect(db, rebind, postgres, …)`
  (resolution.go passes `repo.Rebind` / `repo.IsPostgres()`);
  `NewPersistentStore` keeps the SQLite forms for its existing
  callers. Every statement routes through the rebind; the
  `INSERT OR REPLACE` upsert takes `ON CONFLICT (key) DO UPDATE` on
  PostgreSQL — the insert names every column, so it lands the REPLACE
  row (no foreign keys exist for a cascade to fire through). Noted
  per the survey, not redesigned: on PostgreSQL this cache's table
  lives in the main (SSOT) database, as it already did on SQLite's
  single file; whether a cache belongs there in cluster mode is a P3
  question.
- guardrails: `NewStoreWithRebind(db, rebind)`; both call sites
  (router.go, handlers/chat/guardrails_outbound.go) pass
  `repo.Rebind`. The one statement is standard SQL; only its
  placeholders needed the rewrite.

#### Closure find: batch6_morph.go was not bind-free

The survey (§4.6) listed batch6_morph.go as a RawDB consumer with no
SQL of its own; it has one: `queryMorphUsageRows` builds
`timestamp >= ?` / `LIMIT ?` binds and ran them on the raw handle —
a live PostgreSQL 500 on `/api/morph/usage/stats|requests`. It now
takes the Repo (the stats handler's bind-free COUNT read moved with
it). Both endpoints answer 200 with identical bodies on both backends.

#### Verification (direct checks only; no test suites run)

- **Build gate:** `go build ./...` and `go vet ./...` green; gofmt
  clean on all 27 touched files (the three remaining gofmt hits in the
  tree — app/server.go and two db test files — are unformatted at HEAD
  and untouched). All scratch probe harnesses were deleted before the
  gate, so the gated tree is the committed tree.
- **Settings import parity (HTTP, real binaries):** one fixed payload
  — duplicate connection id, duplicate apiKey *key* under a new id
  with child access rows, duplicate apiKey id (access lists union),
  duplicate combo *name* under a new id, duplicate combo id,
  duplicate kv composite key, node limits, settings — imported on
  SQLite-HEAD, SQLite-post-change and PostgreSQL, followed by a second
  import over the restored state; export read-backs and full table
  dumps (all seven tables plus settings) compared as canonical text:
  - SQLite post-change vs HEAD: **identical**, exports and dumps.
  - PostgreSQL vs SQLite-post-change: **identical** — exports byte for
    byte (after the selectRows fix above; before it, the export diff
    was the column-folding defect), dumps identical modulo the dump
    tool's own display of PostgreSQL's folded column *names* (stored
    values all equal, orphan access rows and unions included).
- **OAuth + stores parity (handler-level probes, one fixed script per
  package, run per backend, canonical read-backs diffed):**
  `saveAxonConnection` insert + update arms (stored refresh token
  survives an update that omits it), `saveDeviceConnection` generic /
  muse (second login deactivates the first row) / zcode (second login
  reuses the row and its stored identity) arms,
  `Repo.SetConnectionProjectID`, the semanticcache store (upsert arm,
  RecordHit, Delete, InvalidateByModel, hydrate round-trip), the
  guardrails store (scope lists, disabled row excluded, Resolve) —
  **SQLite vs PostgreSQL output identical** in all three packages.
- **PostgreSQL boot smoke (final tree):** fresh `router9_test` schema,
  boot log carries **zero** dialect lines (no SQLSTATE / syntax /
  missing-relation hits); the only WRN is the pre-existing vault
  master-key advisory on a fresh DATA_DIR.
- **End-to-end PostgreSQL chat (plan §8):** connection seeded through
  the C4 settings import itself, key created via `POST /api/keys`,
  local mock upstream: non-stream → 200 `upstream reply`, usage
  11/7/18; stream → 200, deltas assembling `streamed reply` + `[DONE]`;
  the mock saw the bare model (`deepseek-chat`) and the connection's
  credential; PostgreSQL read-back `usageHistory = 2`,
  `requestDetails = 2`.
- **Closure census (Tier A markers + raw-handle execution, whole
  tree, non-test):** every remaining hit is one of —
  (a) the SQLite arm of a dialect branch or of the jsonexpr vocabulary
  (C1–C4 files; each guarded by a dialect check or overwritten under
  one — sampled and read, not assumed);
  (b) dialect-gated infrastructure the PG bootstrap never runs:
  db/schema.go (SQLite DDL + seeds), db/client.go (SQLite DSN
  pragmas), db/schema_pg.go (the PG side + DDL translator) — P4
  deletes the SQLite half;
  (c) PRAGMA-class sites inside dialect branches from C1/C3
  (apikeys.go budget probe, usage_retention* maintenance/size report);
  (d) foreign-file readers: browsercookies, oauth/special.go,
  oauth/batch3_zed_keyring.go, oauth/batch3_mimo_desktop.go;
  (e) db/dbtest and all `*_test.go` — open decision D1's territory;
  (f) bind-free statements on the settings transactions (the wipe
  DELETEs, `WHERE id = 1` reads) and the two store handles whose
  statements pass through their rebind — dialect-neutral as written.
  **No unported runtime site remains outside (a)–(f). The P1 dialect
  sweep is complete:** C1 usage, C2 accounts/connections, C3 combos +
  apikeys + rest of internal/db, C4 handler stragglers. What follows
  is P2 (the migrate-db tool) and P4 (zero-SQLite removal), which own
  deleting the SQLite arms and gated files above.
