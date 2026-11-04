# 932 — P6: docs & README rewrite for the PostgreSQL / Docker world

Master plan §P6. Every operator- and developer-facing document now
describes the shipped product: PostgreSQL-only storage (`DATABASE_URL`
required, `JWT_SECRET` from the environment), the memory/Valkey/PG
speed layer, Docker delivery (single + cluster compose stacks, migrate
profile in both), and a CLI reduced to `version`/`update`/`help` beside
the foreground server. Claims were grounded in the current tree (P5
compose files, `.env.example`, `Dockerfile`, CLI help output, the
928/930/931 fragments, and the code) before being written.

## Per-document actions

| Document | Action |
| --- | --- |
| `README.md` | **Full rewrite** (was v1.9.1-era: curl/binary install, embedded file DB, subcommand surface). New: what it is; Docker quickstart for both stacks; configuration-by-env table; "Upgrading from the SQLite era" via the migrate profile (replaces the deleted "sharing a database with 9Router" section); API surface incl. batch-9/10 routes; native development; CLI surface as it exists. |
| `DATABASE.md` | **Full rewrite** (was "SQLite Database and Operator Contract"). New: bootstrap model (no migration runner; catalog read once per boot), type vocabulary, table inventory, monthly partitioning of the two hot ledgers, pool sizing, settings-driven retention + carryover ledgers, the speed-layer degradation model, the legacy migrator contract, `pg_dump`/`pg_restore` backup & restore, the DATA_DIR volume as the backup's other half. |
| `ARCHITECTURE.md` | **Rework.** System-context diagram gains PostgreSQL + the speed layer; repository-boundaries table updated (`internal/db` = PG repositories, new rows for `internal/speedlayer` + `internal/foreigndb`, daemon/service demoted to tray/updater libraries); Fx lifecycle rewritten (foreground only, 30 s drain via `SHUTDOWN_GRACE_SECONDS`); JWT section corrected to env-only/ephemeral; the "SQLite compatibility and bootstrap limitation" section replaced by "Data layer, speed layer, and cluster topology" (incl. the cluster mermaid + what deliberately stays out of the speed layer); build/test section corrected (see test-fleet note below); caveats updated (multi-node writes are a supported shape now). Version header corrected (VERSION 1.9.16-exp.1 / stable 1.9.15). |
| `AGENTS.md` | **Fact updates only** — list below; all rules preserved verbatim otherwise. |
| `CLAUDE.md` | The file is mostly graphify/RTK standing content with **no** stack claims; the one falsified surface was its silence — a stack note matching AGENTS.md's new facts was added under the title. Nothing else touched. |
| `docs/BUILD_DASHBOARD.md` | **Full rewrite** (was a SQLite-era scaffold guide). New: the Dockerfile as the build (3 stages), native dev build, dashboard architecture + ratchet pointer, versioning/release facts from `scripts/bump-version.sh` + `release.yml`. Its old `/api/hello` smoke claim was **not** carried over (unverified; the healthcheck uses `/api/auth/status`, and `/health` is a real route). |
| `docs/DASHBOARD_PROVIDER_PARITY.md` | **Status refresh**: banner recording what batches 1–10 + P0–P5 superseded (mimo-free removal vs the Kelompok-3 `mmf` note; OIDC dashboard SSO deletion vs the Kelompok-7 note; translator steps 2–3; the stack change). Body kept as the September working record. |
| `docs/PARITY-REMAINING.md` | **Status refresh**: banner scoping it as the *upstream*-parity backlog snapshot (2026-09-28), distinct from the Axon-parity axis, with the stack caveat. Body kept. |
| `docs/TELEGRAM_RELEASE_NOTIFICATIONS.md` | **Update**: header note tying the doc to the current image-based release flow (notify runs after the docker job; channel tags; local dev uses compose instead). The body was already accurate and is unchanged. |
| `docs/provider-limits-plan.md`, `docs/smart-combo-plan.md` | **Status-marked implemented** (verified in tree: `internal/db/provider_limits.go`, `internal/proxy/smart/`). |
| `COMPARISON.md`, `ROADMAP.md`, `docs/keirouter-port-plan.md`, `docs/keirouter-port-screenshots.md`, `docs/prompt-injection-playground-plan.md`, `docs/parity/{benchmark-method,rollout-runbook}.md`, `docs/specs/*.md` (2), `docs/superpowers/plans/*.md` (9), `docs/superpowers/specs/*.md` (10) | **Status-marked** as dated snapshots/plans with the stack caveat; bodies untouched. |
| `docs/benchmarks/responses-compression-harness.md` | **Deletion planned by §P6 — not needed**: the file does not exist anywhere in the tree (verified: `find` + `git ls-files`). Nothing purely-SQLite-era remained to delete in `docs/`; the SQLite-era *documents* (old README/DATABASE/BUILD_DASHBOARD/wiki pages) were rewritten instead of deleted because their slots are the current docs' homes. |
| `benchmark/RESULTS.md` | **Status-marked** historical (2026-07-18 run); its harness scripts are named as not running against the current server (see Reported, below). |
| `wiki/*.md` (the published-wiki source) | The §P6 list didn't name `wiki/`, but it is user-facing documentation and was pervasively falsified; leaving it would have voided the grep proof. Actions: `database.md` **rewritten** (PG reality + migration source story); `cli.md` **section surgery** (daemon/service/MITM sections deleted — those commands are gone; menu corrected to menu.go's real 3 entries; env table corrected: `DB_PATH` row → `DATABASE_URL`, duplicate `JWT_SECRET` rows merged to the env-only truth, `9ROUTER_BACKGROUND` row removed; a Supervision section added); `installation.md` **section surgery** (defaults table, DATABASE_URL examples, background/service sections replaced by a persistence note, the bare `docker run` block — which cannot work anymore — replaced by the compose quickstart, source-build run note); `client-setup.md`, `parity.md`, `settings.md` **line fixes** (env rows, the shared-database claim, DB stats wording). Remaining wiki `sqlite` mentions: the migration section of `database.md` and `parity.md`'s *upstream* column (upstream does use SQLite — true). |

## AGENTS.md fact changes (complete list)

Everything else in AGENTS.md is byte-identical; the provider-isolation
§3, parity protocol §6–§7, fragment discipline §7.6, and PR targeting
§7.7 are untouched.

1. **§1.2** — "SQLite database compatibility (`~/.9router/db/data.sqlite`)"
   → data-shape compatibility; the legacy file is a migration *source*
   only (`tools/migrate-db`, `internal/foreigndb`).
2. **§2 mapping** — `internal/db/` row: "SQLite database layer
   (modernc.org/sqlite)" → PostgreSQL layer (pgx, `DATABASE_URL`,
   `schema_pg.go`/`schema_pg_defs.go`).
3. **§2 mapping** — `cmd/9router-go/main.go` row: "runtime daemon" →
   foreground server; subcommands `version`/`update`/`help` only.
4. **§4.A** — heading "SQLite Access" → "Database Access (PostgreSQL)";
   `EXPLAIN QUERY PLAN`/`SCAN <table>` → `EXPLAIN (ANALYZE, BUFFERS)` /
   `Seq Scan`; the dead `internal/db/schema.go` + `supersededIndexes`
   references → the partitioned parent's declared indexes in
   `schema_pg.go`; the two named plan-pinning tests flagged as currently
   sitting in the quarantined fleet; the "SQLite re-evaluates correlated
   subqueries" framing → engine-neutral (the measured incident stands).
   The rule itself is unchanged.
5. **§5.C.4** — ":memory: SQLite / temporary database files" → real
   PostgreSQL test database via `DATABASE_URL`, rows cleaned up; the
   `sqlite_legacy` quarantine (D1) named.
6. **§5.D** — the intro's "temporary SQLite database" → "temporary
   database", plus a **Harness status** note: the suite's bootstrap
   still calls the removed `db.OpenDatabase`
   (`internal/integration/harness_test.go:79`), so the suite **does not
   compile** post-P4 — proven in this phase:
   `go vet -tags integration ./internal/integration/` →
   `harness_test.go:79: undefined: db.OpenDatabase` (81 of 82 files carry
   the tag). Porting it is D1 future work; §5.D's writing rules are
   preserved as the port's spec.
7. **§8 cheat sheet** — `make dev` annotated with the PostgreSQL
   requirement; compose quickstart + migrate-profile commands added.

## In-product text sweep

Grep-driven (`sqlite`, `WAL`, `DB_PATH`, `data.sqlite` over `web/src`,
CLI strings, boot/error strings, comments). **Zero non-test Go files in
the main module contain the literal `sqlite`** (any case) — the code
phases had already scrubbed strings *and* comments; the remaining
mentions were all in the dashboard UI text:

**Fixed (text only, no logic):**

- `web/src/components/SettingsView.svelte` — subtitle "persistent SQLite
  state" → PostgreSQL; the "Default Database Location" card
  (`~/.9router/db/data.sqlite`, "SQLite WAL • Configurable with DB_PATH",
  the false "no schema migration" line) rewritten to the DATABASE_URL /
  additive-bootstrap truth.
- `web/src/components/ProfileSettingsView.svelte` — the Runtime Storage
  card (same falsified block) rewritten; the VACUUM confirm dialog
  ("Compact SQLite database (VACUUM)? … return unallocated pages") now
  states the PostgreSQL truth (the endpoint is a documented no-op;
  autovacuum manages space); the button label "Compact Database
  (VACUUM)" → "Vacuum Database". The endpoint and its success path are
  unchanged.
- `web/src/api/client.ts` — comment "single SQLite transaction" →
  "single database transaction".
- `.github/ISSUE_TEMPLATE/parity_issue.yml`,
  `.github/ISSUE_TEMPLATE/feature_request.yml`,
  `.github/PULL_REQUEST_TEMPLATE.md` — "the SQLite schema (at
  `~/.9router/db/data.sqlite`)" checklist wording → PostgreSQL/database
  schema.

**Reported, not changed:**

- **Stored-data keys `mitmAlias` / `mitmSudoEncrypted`** (deferred from
  P5): they persist as keys inside old databases' `settings` rows. They
  are data, not text — no code reads them since the MITM deletion, and
  this phase does not migrate stored data. Status: inert leftovers,
  harmless; a future settings-scrub could drop them.
- **Makefile staleness (build config, not docs):** targets
  `mitm-enable`/`mitm-disable`/`mitm-status` invoke the deleted `mitm`
  subcommand; the `docker` target runs bare `docker compose up -d` but
  `docker-compose.yml` no longer exists (superseded by the single/cluster
  files). Flagged for a Makefile fix pass; the docs written in this phase
  never reference those targets as working.
- **Makefile:148 comment** — describes the quarantined live tests reading
  credentials from `~/.9router/db/data.sqlite`; that fleet is behind
  `sqlite_legacy`/`ignore` tags and never runs in the default build. Left
  as an accurate description of the quarantined harness.
- **`benchmark/` harness scripts** (`run_benchmark.sh`,
  `run_comparison.sh`, `run_perf_test.py`, `live_provider_smoke.py`) and
  **`scripts/deepseek-web-live.sh`** seed/read a legacy database file to
  drive the server; they cannot work against the PostgreSQL-only binary.
  Porting them is code work, out of a docs phase; RESULTS.md carries the
  status banner.
- **`.github/workflows/ci.yml` comment (~line 518)** quotes a historical
  module-proxy error naming `modernc.org/sqlite@v1.54.0` — a truthful
  anecdote about a past run, left as history.
- **`web/scripts/svelte-check-baseline.json`** pins `totalErrors: 75`,
  but pristine HEAD measures **77 errors / 57 warnings** (this phase,
  machine output, `--tsconfig tsconfig.app.json`): the baseline file is
  stale by 2 relative to HEAD — pre-existing, not caused here (see
  Verification). Also, the ratchet script spawns `bunx`, which does not
  exist on this VM (only `bun`), so `make vet-svelte` fails closed here
  with "svelte-check produced no output" — an environment gap the parent
  may want to close (a `bunx` shim or a script tweak).

## Final grep proof

Command (repo root, this tree):

```bash
grep -rli "sqlite" --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=dist .
```

Survivors, categorized — every one accounted for:

| Where | Why it survives |
| --- | --- |
| `tools/migrate-db/**` (6 files) | The migrator: the one shipped component that reads the legacy format as a **source**. Separate module/image, never linked into the gateway. |
| Migration/upgrade documentation | `README.md` ("Upgrading from the SQLite era"), `DATABASE.md` §7, `ARCHITECTURE.md` (legacy-data paragraph), `wiki/database.md` (migration section), `AGENTS.md`/`CLAUDE.md` (the migration-source facts), `docker-compose.single.yml` / `docker-compose.cluster.yml` / `Dockerfile` / `.env.example` / `.gitignore` comments around the migrate profile and `LEGACY_DIR`. Describing the import path requires naming its source format. |
| `internal/foreigndb/**` + `internal/browsercookies/*_test.go` | **Legitimate survivor, named explicitly:** a read-only reader for *other applications'* files in that format (browser cookie stores, Cursor/Zed state) behind the import-from-browser features. It never opens this gateway's data — the gateway has none. (In non-test Go source the package avoids even the literal word; the mentions live in its tests/fixtures.) |
| The `sqlite_legacy` test quarantine | **300 test files** carry the `sqlite_legacy` build tag (decision D1(a)); they are excluded from the default build. Of the 307 test files mentioning the word, the remaining 7 are: the browsercookies/foreigndb tests above (2), the `integration`-tagged suite (4 — its harness port is the same D1 follow-up), and `internal/handlers/chat/live_guard_test.go` (1 — an untagged guard whose comment names the legacy credential file the quarantined live fleet reads; referencing it is the guard's job). |
| Historical records | `.changes/9*.md` fragments (the phases that removed it), `CHANGELOG.md`, `TECHNICAL_DEBT.md`, status-marked plan/snapshot docs (`COMPARISON.md`, `ROADMAP.md`, parity docs, keirouter/prompt-injection/parity-runbook docs), `benchmark/RESULTS.md` (dated run record), the `ci.yml` anecdote comment. History is not rewritten. |
| Product code & UI | **Zero.** Non-test Go files in the main module: `grep -rl -i sqlite internal cmd --include="*.go" \| grep -v _test.go` → **0 files**. Dashboard source (`web/src`): 0 mentions after this phase's fixes. |

## Verification (direct checks only; no test suites run)

- **README quickstart cross-checked token by token** against the P5
  sources: compose file names, service names (`app`, `postgres`,
  `valkey`, `nginx`, `migrate` — greped from both compose files), the
  `migrate` profile name, and every env var named in the README
  (all present in `.env.example`, except `NODE_ID` and
  `PG_POOL_MAX_OPEN`/`PG_POOL_MAX_IDLE`, which are code-level knobs —
  verified in `internal/speedlayer/config.go` and
  `internal/db/client_pg.go` — that `.env.example` doesn't list).
- **Internal links/paths** referenced by the rewritten docs exist
  (`DATABASE.md`, `ARCHITECTURE.md`, `docs/BUILD_DASHBOARD.md`,
  `tools/migrate-db/Dockerfile`, compose files, `.env.example`).
- **`go build ./...` green** (no Go files were modified in this phase;
  run anyway as the gate).
- **Dashboard:** `bun run build` succeeds with the text edits;
  svelte-check (machine output, ratchet's own invocation reproduced
  directly because of the `bunx` gap above) measures **77 errors / 57
  warnings — and the sorted error/warning sets are byte-identical to a
  pristine-HEAD worktree run** (baseline comparison via `git worktree`,
  never stash): zero added.
- No Docker stack was run in this phase (P8 owns the drill).

## Flagged for P8 (claims only a stack run can settle)

- The README/DATABASE backup & restore command sequence (`pg_dump` /
  `pg_restore` through `docker compose exec`/`cp`) is written from the
  compose service names and standard tooling but has **not** been
  executed against a live stack in this phase; P8 should run it once
  end-to-end.
- The cluster degradation narrative (Valkey kill → PG serving → heal) is
  documented from the P3 live proof (fragment 928); P8's drill re-proves
  it on the shipped stacks.
