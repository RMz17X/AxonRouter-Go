# WS2 foundation: PostgreSQL connection layer + schema port

First step of the PostgreSQL migration (master plan
~/workspace/9router-go-upgrade-plan.md, phase P0). Transitional by
design: the SQLite backend remains selectable and unchanged in
behaviour; a later phase removes it.

**Connection layer** (`internal/db/client_pg.go`, +8 lines in
`client.go`): a non-empty `DATABASE_URL` makes `InitGlobalDatabase`
open PostgreSQL through the pgx stdlib driver (pool 10/5, 30 s
statement timeout unless the DSN sets its own). The backend is a
property of the handle, not of configuration: `DialectOf` decides from
the driver, `Repo` caches it (`Repo.Dialect`, `Repo.IsPostgres`), so a
handle is always read as what it is — including SQLite handles tests
open in the same process.

**Schema port** (`internal/db/schema_pg.go`, `schema_pg_defs.go`;
+6 lines in `schema.go`): `EnsureCoreSchema` dispatches PostgreSQL
handles to `EnsurePostgresSchema` — the same table set in PostgreSQL
dialect: payload columns as jsonb, counters as bigint, costs as double
precision, identity primary keys, the SQLite indexes carried over.
`usage_history` and `request_details` are monthly range-partitioned
parents; boot ensures the current and next month's partitions. SQLite
handles keep the original bootstrap byte-for-byte.

Verification (direct checks, no test suites): `go build ./...` green;
cross-compile of the pgx graph + `internal/db` green for linux-amd64,
linux-arm64, darwin-amd64, darwin-arm64, windows-amd64 (all CGO-free).
Fresh-database boot against PostgreSQL 17 (new empty database):
schema created — 22 tables + `upstream_leases`, 2 partitioned parents
with monthly partitions, jsonb/bigint/double column types confirmed
from the catalog — and the server answers HTTP 200. SQLite regression
boot unchanged (HTTP 200, data.sqlite created).

Known gaps, owned by the dialect sweep (plan P1): queries outside this
foundation still speak SQLite dialect and fail soft on PostgreSQL —
seen in the boot log as warnings: the legacy provider-id migration
(`syntax error at or near "WHERE"`) and thought-signature hydration
(`syntax error at or near "ORDER"`). Both are logged, neither blocks
boot; neither runs on the PostgreSQL backend until P1 ports them.
