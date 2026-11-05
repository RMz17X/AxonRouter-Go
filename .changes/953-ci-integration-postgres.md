## CI: the integration job gets its PostgreSQL

**Symptom.** The CI `integration` job was permanently red: every DB-backed
test failed at 0.00s with "neither INTEGRATION_DATABASE_URL nor
DATABASE_URL is set", which read as dozens of broken tests and hid real
regressions behind the noise.

**Root cause.** A workflow-config defect, not code: the job declared no
PostgreSQL service and no DSN environment, while the suite (internal/db/
dbtest) requires a reachable scratch PostgreSQL to provision its throwaway
schemas. The job could never have passed.

**Fix.** The job now runs a `postgres:17` service container (database
`axonrouter_integration`, health-gated on `pg_isready`) and exports
`INTEGRATION_DATABASE_URL` for its steps, matching the DSN contract the
harness already implements.

**Verification.** The identical suite was run locally against a scratch
database with only `INTEGRATION_DATABASE_URL` set — the configuration the
workflow now provides — and passes; the workflow change is a transcription
of that proven setup.
