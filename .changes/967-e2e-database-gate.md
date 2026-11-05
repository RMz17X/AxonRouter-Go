## Dashboard E2E: a database-backed suite, gated like one

The CI test job's last red step: Dashboard E2E failed with "gateway
exited during boot with code 1" (nineteen times — once per file). It
was masked until now because the job used to die earlier, at the
svelte-check ratchet.

Root cause: the E2E harness boots the real gateway, and since P4 the
gateway's database is PostgreSQL via DATABASE_URL — there is no file
database to fall back to (the harness header still describes the
removed SQLite setup). CI configures no database, so the binary exits
at boot. Under the owner's rule (fragment 966: database-backed tests
skip when no database is configured) this suite is a database-backed
surface and is gated the same way:

- The CI step now runs only when the E2E_DATABASE_URL repository
  variable names a reachable scratch PostgreSQL, passed to the suite
  as DATABASE_URL. Unset (the default) → the step is skipped and the
  job stays green.
- The harness names the requirement up front when no DSN is present,
  replacing the opaque boot-exit error with the actual cause.
