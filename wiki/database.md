---
title: Database
order: 11
section: Under the Hood
---

# Database

9router-go stores everything in **PostgreSQL** — there is no database file.
The server connects with `DATABASE_URL` and refuses to boot without it.

## Where the data lives

- **PostgreSQL** holds all durable state: settings, provider connections,
  API keys, combos, and the usage ledgers. In the Docker stacks it is the
  `postgres` service and its `pgdata` volume.
- **`DATA_DIR`** (the `appdata` volume in Docker) holds non-database state
  only: uploads, CLI tokens/machine-id, provider instruction files, tunnel
  state, and the model-catalog cache.

The schema bootstrap is additive and runs on every boot — missing tables
and columns are created, the current and next month's partitions of the
two hot ledgers (`usageHistory`, `requestDetails`) are ensured, and seed
rows are inserted. A fresh empty PostgreSQL database initializes itself;
there is no versioned migration to run. The full contract is `DATABASE.md`
in the repository.

## Coming from the SQLite era

Older installs kept everything in `~/.9router/db/data.sqlite`. That file
is now a **migration source only**: the compose `migrate` profile
(`tools/migrate-db`, a separate tool that is never part of the server)
copies it into PostgreSQL with verification, preserving ids, timestamps,
and payloads. Sharing one database file with the Node.js 9Router is no
longer possible — the two products' storage has diverged. See the
"Upgrading from the SQLite era" section of the repository README for the
steps.

## Backup

Back up the database with `pg_dump` and the `DATA_DIR` volume with any
file backup tool:

```bash
docker compose -f docker-compose.single.yml exec postgres \
  pg_dump -U 9router -d 9router -Fc -f /tmp/9router.dump
```

The dashboard's Download Backup is a different thing: a portable payload
of your dashboard data (settings, connections, keys), not a database
backup.

## Binary portability

One binary per target, built from pure Go source with `CGO_ENABLED=0`. Every
release target must still build — that is the only thing constraining a
dependency here, not its size.
