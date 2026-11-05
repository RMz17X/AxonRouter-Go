## Database-backed tests skip when no database is configured (owner rule)

Owner order (2026-11-06): "test yang butuh db itu di skip aja" — a
test that needs a database should skip, not fail, when no database is
configured. CI therefore provisions no database again: the postgres
service fragments 953/965 added to the integration job is removed,
and the job returns to its plain shape.

- dbtest gains a sentinel, ErrNoDatabase, returned (wrapped) by
  BaseDSN when neither INTEGRATION_DATABASE_URL nor DATABASE_URL is
  set. It is deliberately distinct from a configured database that
  fails to connect or provision — that stays a failure.
- The integration harness skips on ErrNoDatabase (t.Skip) instead of
  failing at provision; bootfx's TestMain reports the skip and exits
  clean, since every test in that package needs the boot database.
- Wherever a DSN IS configured the suite runs in full, unchanged —
  verified both ways locally: no env → SKIP + package ok; env set →
  the same tests pass.
