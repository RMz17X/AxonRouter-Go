# 1016 — Usage Overview topology retention is 10 minutes, end to end

## Symptom

On /dashboard/usage, a provider dropped off the topology map after five
quiet minutes. The owner raised the retention to a full 10 minutes
(2026-11-08), superseding his 2026-11-04 five-minute rule.

## Root cause

Two layers clipped presence at or below the old window:

- Frontend: `TOPOLOGY_RETENTION_MS` in
  `web/src/components/analytics/AnalyticsView.svelte` was `5 * 60_000`,
  and the presence derivation (`topologyProviders`) gates every source on
  it.
- Backend: the derivation's durable source is
  `stats.byProvider[key].lastUsed` from `GET /api/usage/stats`, but whole
  period families could not supply it. Daily windows (7d/30d/60d/all)
  aggregate `usageDaily` rows, which are keyed by date and carry no time
  of day, so their `byProvider` entries always had an empty `lastUsed`;
  the "today" window starts at midnight. On a freshly loaded page with a
  daily period selected, a provider whose last request was 6–9 minutes
  old therefore had no `lastUsed` at all and survived only if it happened
  to sit in the recent-requests backstop (the latest 60 raw rows).

## Fix

- `AnalyticsView.svelte`: `TOPOLOGY_RETENTION_MS` is now `10 * 60_000`;
  the presence-rule comments state the 10-minute rule dated 2026-11-08
  superseding the 2026-11-04 five-minute rule.
- `internal/handlers/usage_stats.go` (`HandleUsageStats`): after the
  period window is aggregated and before totals are summed, overlay each
  `byProvider` entry's `LastUsed` with the provider's newest raw
  `usageHistory` timestamp inside the retention window
  (`Repo.GetUsageGroupByProviderSince(now − 10m)`, answered by the
  `idx_uh_ts_prov` index). The overlay only raises entries the selected
  window already aggregated — it never creates one — so period totals
  and breakdown membership are unchanged; a provider quiet for longer
  than 10 minutes keeps its previous (possibly empty) `lastUsed` and
  expires from the map on the normal rule. Side effect, intended: in a
  daily period, the provider breakdown's Last Used cell now shows a real
  timestamp for a provider active within the last 10 minutes instead of
  staying blank; providers quiet longer stay blank there, as before.

## Verification

- Web build via the heavy gate: green.
- `go build ./...` via the heavy gate: green.
- svelte-check (`--tsconfig ./tsconfig.app.json`): baseline
  89 errors / 56 warnings, unchanged.
- Fresh-load case traced end to end: for the default "today" and "24h"
  periods the raw window cache already fills `lastUsed` with the newest
  in-window request; for daily periods the new overlay supplies it for
  any provider active within the retention window; the recent-requests
  stamps remain as a backstop in both paths.
- Existing test expectations re-checked by reading (suite not run, per
  the standing rule): `TestHandleUsageStats_DailyByProviderHasNoLastUsed`
  (sqlite_legacy-quarantined) seeds no `usageHistory` rows, so the
  overlay is a no-op for its fixture and its expectation still holds; no
  web test asserts the retention window.
- Other 5-minute constants in the tree (combo failover grace, account
  pacing cap, latency window default, analytics chart labels) are
  different rules and are untouched.
