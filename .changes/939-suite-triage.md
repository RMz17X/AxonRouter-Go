### Integration suite triage (council round 1, item F-1)

- The `integration`-tagged suite was executed end to end at the production-ready
  gate (baseline: 33 failures plus a 600 s package timeout) and now completes
  green: `go test -tags integration -race -timeout 30m ./internal/integration/...`
  exits 0 — 250 pass, 0 fail, 6 env-gated live tests skipped — in ~657 s on the
  4 vCPU VM. Full record: `~/workspace/9router-suite-triage.md`.
- No shipped-gateway defect was found: every baseline failure was a test
  asserting a superseded contract. Create endpoints answer Axon's 201 (batch 9,
  fragment 926 D1/D10) where helpers expected 200; the combos list travels in
  the `{combos}` envelope and `/v1/models` in exactly `{object, data}` (926
  D10/E1) where tests decoded a bare slice and a `connections` count;
  `/v1/models/info` serves the batch-10 Axon document (929 item 1), so the
  single-model test asserts `contextWindow` and the custom-model test pins
  Axon's 404 for catalogue-absent ids. The batch test's nameless line now pins
  batch 9's 400 "Name is required" instead of the pre-parity acceptance.
- SQLite-era seed code was ported to PostgreSQL: `?` placeholders became `$n`
  in raw seed SQL (PostgreSQL parses `?` as an operator), empty-string JSON
  seeds for the JSONB `usageHistory.meta` column became `'{}'`, JSONB
  read-backs are asserted on the parsed document rather than one text
  spelling, and the bootfx schema subtest queries `pg_tables` instead of
  `sqlite_master`. Tests that seed historical usage rows provision that
  month's partition via a new harness helper — the schema deliberately
  ensures only the current and next month and has no DEFAULT partition, the
  same on-demand step `tools/migrate-db` performs per migrated month.
- One real defect, in test infrastructure: `dbtest.NewSchemaDSN` built its
  DSN with `pgx.Config.ConnString()`, which does not serialize RuntimeParams,
  so the `search_path` was silently dropped and bootfx ran against the
  scratch database's `public` schema instead of its throwaway schema. The
  DSN now carries `search_path` explicitly; bootfx passes on repeat runs
  and leaves `public` untouched.
- `TestProviderLevelPoolReachesBespokeTransport` was deleted: its subject,
  the mimo-free bespoke lane, was removed by owner order (fragment 914).
  Pool assignment stays pinned by the shared-client and custom-executor
  cases in the same file.
- The bootfx usage-history subtest now awaits the batched async ledger
  writer (bounded 5 s poll, assertion unchanged), and the baseline package
  timeout is closed as aggregate `-race` slowness (~650 s package), not a
  hang: the completing runs use `-timeout 30m`.
