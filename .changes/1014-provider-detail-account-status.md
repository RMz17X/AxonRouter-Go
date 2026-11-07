# 1014 — Provider detail page shows account/API-key health (error, cooldown, locked, disabled)

## Symptom

- On a provider detail page (e.g. /dashboard/providers/tokenharbor) there was
  no way to see at a glance which of the provider's accounts/API keys were in
  error, in cooldown, locked, or disabled. The rows showed a probe-driven
  badge (testStatus + a live one-by-one probe), and the server-computed
  `effectiveStatus` was not consumed by the frontend at all.
- `GET /api/connections` could emit `effectiveStatus: "<nil>"` with
  `isEffectivelyActive: false` for healthy, active API keys.

## Root cause

- `sanitizeProviderConnection` (internal/handlers/dashboard/connections.go)
  computed the status with `fmt.Sprintf("%v", safe["testStatus"])`. The
  sanitized map only carries `testStatus` when the stored blob recorded a
  verdict (or the fatal-disable path forced one), so for never-probed
  connections the key was absent and `%v` of a nil interface produced the
  literal string `"<nil>"`, which the status engine then returned verbatim
  as the effective status. Connections with an empty/unreadable data blob
  also returned early with no status fields at all.
- The detail view derived its row badges purely from `testStatus` + the
  one-by-one probe state, so server verdicts (quota_exhausted,
  rate_limited, unavailable, expired — including ones derived from
  lastError classification) never surfaced, and live model locks showed
  only as a ⏱ countdown with no model name or expiry.

## Fix

- Backend (connections.go): the test status is now read as a string
  (`safe["testStatus"].(string)` semantics — a missing verdict reaches the
  status engine as `""`, the never-probed case) via a shared
  `decorateEffectiveStatus` helper, and the decoration now runs on every
  return path of `sanitizeProviderConnection`, including the empty/invalid
  data early returns.
- Frontend:
  - `client.ts`: `ProviderConnection` gains the server status fields
    (effectiveStatus, isEffectivelyActive, statusVariant, disabledReason,
    disabledAt, rateLimitedUntil, backoffLevel), plus a
    `getModelAvailability()` wrapper for the locks census
    (GET /api/models/availability) with its `ModelAvailabilityEntry` type.
  - New `connectionHealth.ts`: one health state per connection
    (active / error / cooldown / locked / disabled) derived from the
    server `effectiveStatus` with the precedence of
    internal/providers/connection_status.go (disabled toggle first, then
    the throttle verdict, then the error family), with live locks merged
    from the census rows and the connection payload's own `modelLock_*`
    keys (the census SQL prefilter keys on the `modelLocks` object shape,
    so flattened-only locks are supplemented from the payload — the same
    source the row's ⏱ timer already reads).
  - New `ProviderAccountStatusStrip.svelte`: a counts strip
    (Active / Error / Cooldown / Locked / Disabled, with the affected
    account names in each chip's tooltip) rendered above the provider's
    connections list, in the rows' existing badge vocabulary.
  - ProviderDetailView wiring only: fetches the census on the same cycle
    the connections refresh (provider open + parent reloads, no new
    polling), renders the strip, and adds row badges — a lock badge
    naming the locked model (+N more) with remaining time and full
    model/until list in the tooltip (the bare ⏱ lock timer yields to
    it), a cooldown pill for a rate_limited verdict with no recorded
    reset, and the server error verdict when the probe badge called the
    connection "active". The disabled badge gains a "· ToS" qualifier
    when the payload marks an upstream terms-of-service disable.
  - Refresh/liveness: the counts and badges re-derive on the view's
    existing 1s clock, so a lock that lapses while the page is open drops
    out without a refetch.

## Verification

- `go build ./...` and `go vet` on the dashboard handler package via the
  heavy gate: green.
- Web build via the heavy gate: green.
- svelte-check (`--tsconfig ./tsconfig.app.json`): 89 errors / 56 warnings,
  exactly the pre-existing baseline.
- Direct check of the fixed decoration logic: an active connection whose
  stored blob has no `testStatus` now resolves through the
  never-probed branch of EffectiveStatusWithError ("" input) instead of
  returning `"<nil>"`; disabled and fatal-disable paths still force
  `testStatus: "disabled"` before decoration, so their verdicts are
  unchanged.
