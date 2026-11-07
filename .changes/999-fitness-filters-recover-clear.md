# 999 — Fitness tab: filters, Auto-Recover, Clear Cooldowns

## Symptom

The Fitness & Canary tab listed every pool with no way to filter (the JS
fitness tab filters by type and search), and two backend capabilities
had no UI at all: `POST /api/proxy-pools/recover` (auto-recovery probing
of unhealthy pools) and `POST /api/proxy-pools/fitness/clear-all`
(clearing fitness/cooldown marks).

## Fix

Frontend only (`ProxyPoolsView.svelte` + two client methods):

- A filter row: search (name/URL/group) and the same counted Type filter
  as the Pools tab (993), applied to both the cooldown table and the
  fitness leaderboard, with a "Showing N of M" hint while filtering. The
  cluster stat cards stay global.
- "Auto-Recover" calls the recover endpoint (force mode) and reports the
  server's checked / recovered / still-failing counts verbatim;
  disabled when nothing is in cooldown or failed.
- "Clear Cooldowns" calls fitness/clear-all behind the standard confirm
  dialog, making clear that quarantined pools become eligible at once
  and the canary re-quarantines any that still fail.

## Verification

- svelte-check baseline held (89/56, zero new); web build succeeds.
- Endpoint semantics read from the handlers before wiring
  (`batch4_health.go` recover result shape; `batch4_fitness.go`
  clear-all body).
