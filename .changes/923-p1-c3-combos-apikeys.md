# P1 cluster C3 — combos + apikeys + rest of internal/db on PostgreSQL

Master plan §5 (P1); survey: `~/workspace/9router-go-P1-worklist.md`
cluster C3 (221 `?` placeholders). Verified against the LOCAL PostgreSQL
17.11 on this VM (127.0.0.1:15432, `router9_test`); the SQLite path is
byte-identical after the sweep (probe diff below). The step-0 mechanism
(`.changes/921`) is reused throughout: statements keep their `?` text and
route through the `Repo` wrappers / `Repo.Rebind`; dialect branches live
only where the SQL — or the data — itself differs. **This cluster removes
the C1-recorded gate on the end-to-end PostgreSQL chat: `queryApiKey`'s
unrebound `?` was the blocker, and the full request path (key create →
key auth → proxied chat → usage write) now runs on PostgreSQL** (proof
at the bottom).

## The mechanical remainder (placeholders only)

Every statement routed through the wrappers; transaction statements
rebind via `r.Rebind`; no statement's SQLite text changed.

- `dashboard.go` — the rest of the file (C2 ported only
  `UpdateConnectionData`): connection PUT/DELETE surface
  (`UpdateProviderConnection`, `DeleteProviderConnection`,
  `SetConnectionStatus`, `SetConnectionPriority`,
  `ReorderProviderConnections` incl. its tx), combo writers
  (`CreateCombo`/`UpdateCombo`/`DeleteCombo`), the key writers and
  `GetApiKeys`, the kv surface (`GetKVScope`/`SetKV`/`SetKVMany` tx/
  `DeleteKV`), `GetSettingsRaw`. The two `ON CONFLICT(scope, key)`
  upserts were already PG-form; only their placeholders moved.
- `combos.go`, `combos_rules.go` — the combo reads (the worklist's §6
  listed combos.go as bind-free; `GetComboByName`/`GetComboById` do
  bind) and the smart-rules store incl. `ReconcileCombo`'s tx.
- `guardrails.go` — plus one convention fix: `enabled` is an INTEGER
  column in both schemas, but the file bound and scanned it as a Go
  bool. It now binds `boolToInt(...)` and scans through an int, the
  plans.go convention — a `*bool` destination depends on each driver's
  int width, an int destination does not. SQLite behaviour is the same
  values through the same column.
- `jailbreak.go`, `jailbreak_keywords.go` — the kv-backed preset /
  binding / exclude / usage-counter store (all statements are kv reads
  and the `ON CONFLICT` upsert, already PG-form).
- `batches.go`, `files.go`, `aliases.go`, `deprecations.go`,
  `budget_spend.go`, `batch7_mcp_audit.go`, `settings_writes.go` — the
  settings upsert in settings_writes.go is the write every
  `UpdateSettingsRaw` caller stands on; `GetSettingsRaw` (dashboard.go)
  is its read twin.

## The named sites

- **`apikeys.go` — the budget-columns probe.** `PRAGMA table_info`
  has no PostgreSQL form, so `budgetColumnsPresent` branches: SQLite
  keeps the PRAGMA loop verbatim; PostgreSQL asks the same question of
  the catalog (`information_schema.columns` for the folded names the
  unquoted DDL stored, `apikeys`/`limitmicros`). It is a real probe,
  not a dialect constant: a hand-made PostgreSQL schema without the
  F-2 columns still degrades every key read to the legacy query,
  exactly as a pre-F-2 SQLite file does — the failure behaviour the
  worklist said to preserve. The binary-bootstrapped schema carries
  the columns, so PG key reads take the full 24-column form (proven by
  the period-budget round-trip in the probes).
- **`apikeys.go` — the hashed-row sentinel cannot be stored on
  PostgreSQL.** The sentinel prefix is `"\x00argon2id:"`; PostgreSQL
  text rejects the NUL byte outright (SQLSTATE 22021 — the probe's
  first PG run failed `HashApiKeyRow` with it). The PostgreSQL
  spelling is `"\x01argon2id:"`: the same class of character — a
  control byte no HTTP header value can carry, so no client can send
  it either — in a form the column accepts. `HashApiKeyRow` writes the
  spelling its dialect can store (`Repo.hashedKeySentinelFor`);
  `IsHashedKeyRow` recognises both spellings, so a row keeps its
  meaning whichever backend wrote it. The SQLite arm is untouched:
  existing rows and new SQLite writes keep the NUL prefix byte for
  byte.
- **`plans.go`, `modelaccess.go` — the `sqlite_master` probes.**
  `plansTableMissing`, `HasPlansTable` and `HasAllowedModelsTable`
  now go through `tableExistsOnDB` (the C1 helper: `sqlite_master`
  count on SQLite, `to_regclass` on PostgreSQL), preserving each
  caller's contract — for `plansTableMissing`, a failure to answer is
  still an error, never an absence.
- **The three dynamic IN-list builders** (`plans.go`
  `GetPlansForKeys`, `modelaccess.go` `GetAllowedModelsForKeys`,
  `quota_reset.go` `GetApiKeyTokenUsageForKeys`' marks read) build
  their `?` lists exactly as before and let the wrapper rebind the
  finished statement, so the per-element numbering is the rebind's,
  not a hand-rolled `$n` — one mechanism for static and generated
  placeholders alike.
- **`quota_reset.go` — the batch VALUES form needs casts on
  PostgreSQL.** `apiKeyTokenUsageBatchQuery`'s CTE reads its keys from
  a `VALUES` list of bound parameters. A parameter inside a CTE's
  VALUES has no column context to infer a type from, so PostgreSQL
  typed the reset mark as text and the history sum's `u.id > k.mark`
  had no operator (`bigint > text`, SQLSTATE 42883). The PostgreSQL
  rendering (`apiKeyTokenUsageBatchQueryFor`) casts each row —
  `(?::text, ?::text, ?::text, ?::bigint)` — stating the types the
  comparisons already assume. The pinned no-arg function still returns
  the SQLite text verbatim (the query-plan tests assert on it), and
  the reader now calls the dialect form. This was invisible through
  the Repo method alone — it falls back to per-key reads on error,
  which returned the right numbers on PG while the one-statement page
  read (§4.A's contract) silently never ran; the probe therefore also
  ran the statement form directly, which is what caught it.
- **`vault_migrate.go` — the pre-migration backup is file-world
  only.** The seal UPDATE routes through the wrapper. `backupDatabase`'s
  `VACUUM INTO ?` snapshot has no PostgreSQL equivalent: the database
  is a server, not a file under the data directory, and the `dbPath`
  the caller passes names a SQLite file that does not exist in that
  world. The branch skips the snapshot on PostgreSQL and logs that it
  did — the restore point there is the server's own backup, the
  operator's side of a PostgreSQL deployment — rather than inventing
  a file form or failing the migration shut. The sealing pass itself
  is identical on both dialects.

## Verification (direct checks only; no test suites run)

- **Build gate:** `go build ./...` and `go vet ./...` green; gofmt
  clean on all 18 touched files (three — aliases.go, deprecations.go,
  modelaccess.go — were already unformatted at HEAD in the
  trailing-newline class; the gate leaves touched files formatted).
  The scratch probe harness was deleted before the gate, so the gated
  tree is the committed tree.
- **Repo-level parity probes** — one fixed script (~120 operations:
  the full key lifecycle incl. hash/rotate/display and the period
  budget, combos CRUD + smart rules + reconcile, the connection
  writers incl. reorder, kv + settings writes, plans CRUD + batched
  reads, model access, quota reset + token usage + per-period spend,
  guardrail policy/log CRUD, the jailbreak store, aliases,
  deprecations, batches, files, the MCP audit store, the vault no-op)
  run on SQLite-HEAD, SQLite-post-change and PostgreSQL, read-backs
  compared as canonical line sets:
  - SQLite post-change vs HEAD: **identical**, line for line.
  - PostgreSQL vs SQLite-post-change: **identical** except three
    read-backs of jsonb columns (`combos.models`,
    `providerConnections.data`) rendering in PostgreSQL's canonical
    JSON form (`["a/b", "c/d"]` with a space after the comma) — the
    same document, the C1-recorded jsonb rendering class; every
    consumer unmarshals these columns.
  - The probe's first PG run caught the two real defects above
    (sentinel NUL, batch VALUES typing); both are fixed and the
    re-run is clean, `failures: 0` on all three backends.
- **HTTP parity probes** — identical request sequences (combo
  create/read/update/delete, key create/list, key auth against
  /v1/models incl. toggle off → 401 → on → 200 → delete → 401,
  connection create/read/PUT-update/delete — the PUT/DELETE pair C2
  left PG-broken) behind the real binaries on all three backends:
  SQLite post-change vs HEAD **identical**; PostgreSQL identical
  except the one jsonb rendering line. The PostgreSQL boot logs carry
  **zero** dialect warn/error lines; the only WRN is the pre-existing
  vault master-key advisory on a fresh DATA_DIR (the two 401 request
  log lines are the probe's own deliberate auth checks).
- **End-to-end PostgreSQL chat (the cluster milestone, plan §8).**
  Fresh `router9_test` schema + fresh DATA_DIR, `bin-new` on
  PostgreSQL, a DeepSeek connection seeded at a local mock
  OpenAI-compatible upstream (`ALLOW_PRIVATE_NETWORKING=true`), a key
  created through `POST /api/keys`:
  - Non-stream `POST /v1/chat/completions` → **200**, content
    `upstream reply`, usage 11/7/18; the mock's log shows the gateway
    presented the connection's credential and the bare model
    (`deepseek-chat`, prefix stripped).
  - Stream `POST /v1/chat/completions` → **200**, SSE deltas
    assembling `streamed reply`, the usage chunk and `[DONE]`.
  - PostgreSQL read-back: `usageHistory = 2`, `requestDetails = 2`,
    summed prompt+completion tokens **36** (18 + 18) — the full
    request path, key auth included, writes its usage rows into
    PostgreSQL.

## Deliberately left for C4 (named, untouched)

- `handlers/dashboard/settings.go` — the batch-7 credentials-import
  path's eight `INSERT OR REPLACE` statements through `RawDB()` (~62
  `?`): the per-table REPLACE-vs-`ON CONFLICT DO UPDATE` decision the
  survey reserved for C4.
- `handlers/chat/gemini_handler.go` — the one remaining `json_set`
  site (the jsonexpr vocabulary from C2 covers its shape).
- The `handlers/oauth/` connection-upsert family (179 `?`, one
  repeated shape), `semanticcache/persistent_store.go`,
  `guardrails/resolver.go` — the rest of the survey's C4 list.
- The batch-8 remainder routes stay owner-discarded; nothing in this
  cluster revives them.
