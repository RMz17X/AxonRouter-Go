## GET /api/connections: the unfiltered list returns connections again

**Symptom.** GET /api/connections without a `?provider=` filter
returned `[]` with a 200 — visible in the integration suite
(TestConnectionLifecycle, TestConnectionNameConflictIsRefused: a
just-created connection "is not in the list []") and in the dashboard
itself, whose unfiltered callers (App boot, the add-from-link modal)
silently received nothing.

**Root cause.** A perf guard (commit 68e94eb3) short-circuited the
handler to an empty list when no provider filter was given, to avoid
dumping a very large connections table. The guard changed the
endpoint's contract instead of its cost profile, and every caller
that relied on the upstream list shape broke without an error.

**Fix.** The short-circuit is removed: the handler again serves
GetProviderConnections for the filter as given (empty filter = all
connections), with the existing secret-stripping sanitizer unchanged.
The aggregate views that motivated the guard read /api/providers/stats;
the handler comment records the reasoning so the guard is not
reintroduced.

**Verification.** The integration lifecycle and name-conflict tests,
which failed against the guarded handler, pass against a live
PostgreSQL through the real router.
