title: Shutdown under load exits cleanly instead of a fatal deadline error
version: v1.10.43

## Fixed
- An intentional stop (SIGTERM, dashboard shutdown, restart hook) while
  streams were in flight ended with a bare `context deadline exceeded`
  line and exit code 1, indistinguishable from a crash: main gave
  fxApp.Stop a 20s budget while the HTTP server's own drain grace is
  30s, so the stop context always expired first under load. The stop
  budget is now derived from the server grace (app.StopBudget: grace +
  15s headroom for the remaining hooks), and a stop that still outlives
  the budget is logged as a shutdown warning and exits 0 — the stop was
  requested, so it is not a failure. Non-deadline stop errors remain
  fatal as before.
