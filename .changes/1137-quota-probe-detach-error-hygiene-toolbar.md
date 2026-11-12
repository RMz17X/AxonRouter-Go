title: Quota probes detached from browser cancellation, quota errors humanised, Connections toolbar tidied
version: v1.10.45

## Fixed
- A connection's quota (usage) read no longer dies when the dashboard
  request that triggered it goes away. The fetch context was the
  browser request's own context, and the Quota Tracker supersedes its
  passes with an AbortController — one page change or refresh aborted
  the request and killed the in-flight upstream probe mid-call, which
  surfaced on account cards as a raw Go error dump ("membership: Post
  https://…: context canceled", signed URL and user id included). The
  fetch context is now detached (context.WithoutCancel); every upstream
  call keeps its own 30s budget, the per-connection credentials, signed
  request and proxy egress are unchanged, and results are cached
  server-side as before, so a finished probe now survives the browser.
- Quota error messages are cleaned before they reach the UI
  (cleanUsageMessage in the usage response): a dead OAuth grant
  (invalid_grant, "this refresh token can no longer be used") and a
  rejected refresh become one plain sentence — "Sign-in expired —
  refresh token no longer valid. Re-authorize this account." — raw
  JSON bodies and request-URL dumps are cut, interrupted/timed-out
  checks and transport failures get a short retry hint, and wording
  the dashboard's own classifiers key on (terms of service,
  suspension, verification) passes through untouched. The Quota
  Tracker applies the same cleaning (displayQuotaMessage) to the
  stored lastError it restores onto disabled accounts' cards, and
  renders card errors/messages as a tidy icon notice instead of a raw
  centred paragraph.
- Provider Detail → Connections toolbar tidied: the action buttons
  (Apply Proxy, Reset Cooldown / Exhausted, Test Connection
  One-by-One, and the selection/bulk actions) share one height and
  icon size, and Round Robin / Sticky / Custom headers / Proxy Lock
  are grouped into a single settings pill at the right edge that
  wraps cleanly on narrow screens. No behaviour changes.
