# 986 — Quota reads release quota locks on recovery; MiniMax M Plan windows render as progress

## Symptom

Two gaps around the Quota Tracker, reported by the owner:

1. No automatic release: when a quota check showed an account's quota had
   refilled (the owner's example: a MiniMax Global account whose credits
   were back), the account stayed in cooldown / locked / exhausted until
   the timer lapsed or an operator pressed the manual Reset button.
2. MiniMax providers showed no progress UI: the M Plan rate windows
   arrived in the tracker as nameless rows with no bar, no percentage and
   no reset time, while other providers render a progress bar per window.

## Root cause

1. `HandleGetConnectionUsage` treated a usage read as one-way. Its only
   state writes were for bad news (an Antigravity ToS message disables the
   account; a validation message sets the verification flag). A successful
   read that reported quota available changed nothing — the persisted
   `rateLimitedUntil`, per-model locks and the stored quota error all
   survived it. The only automatic releases in the tree were the chat
   success path (a real generation) and the DeepSeek credential renewal.
2. The MiniMax Code usage port stored each parsed M Plan window in the
   quotas map as the internal `miniMaxCodeWindow` struct, whose fields are
   all unexported — so every window serialized to `{}`. Upstream
   (`open-sse/services/minimaxCodeUsage.js`) emits each window as a full
   quota map (`used: 100-left, total: 100, remainingPercentage: left,
   resetAt`), and the frontend's minimax branch and progress-bar rendering
   were already built for exactly that shape; they simply never received
   the numbers.

## Fix

1. New `usage_autorelease.go`: after a **live** usage fetch succeeds in
   `HandleGetConnectionUsage` (the generic provider path, its
   auth-refresh retry, and the Antigravity path on a message-free read),
   `maybeAutoReleaseQuotaLocks` releases the connection's quota lock state
   when the fresh quotas show availability (any unlimited row, any credit
   balance above zero, or any window with `remainingPercentage > 0`).
   The release re-reads the connection, requires stored evidence of a
   quota-class park (a rate-limit timer, `lockedAllUntil`, a per-model
   lock, or a stored error the provider classifier reads as quota
   exhausted / rate limited), and then calls the same two Repo operations
   the DeepSeek renewal uses: `ClearConnectionRateLimit` (also clears the
   stored error and restores status) and `ClearConnectionModelLocks`.
   Boundaries, all deliberate: a stored ToS-disable signature vetoes the
   release; disabled accounts never reach the fetch (the handler returns
   their stored reason first), nothing is reactivated and no verification
   flag is cleared; cached usage responses return before any fetch, so
   only live reads can release; Antigravity's in-memory strike breaker
   (chat-path layer, self-expiring) is untouched — the persisted state is
   what is released. Failures are logged and never fail the usage
   response.
2. `usage_minimax_code.go`: windows are converted with
   `miniMaxCodeWindowQuota` into the upstream quota shape
   (`usageQuota(100-remaining, 100, resetAt)` plus `name` and
   `remaining`), so the tracker's existing minimax branch now renders the
   progress bar, percentage and reset countdown per window. The credits
   row is unchanged: MiniMax reports a balance with no grant total, so it
   stays an amount (`isCreditBalance`) rather than an invented
   percentage.

## Verification

- Temporary in-package probe (run, then removed): availability evaluation
  (MiniMax window with quota left → available; all windows at zero → not;
  credits balance > 0 → available; balance 0 → not; unlimited → available),
  lock-state gate (rate-limit timer → release; model lock → release;
  stored "quota exceeded" error → release; stored ToS signature →
  vetoed; clean data → no release), and the window conversion (remaining
  80 → used 20 / total 100 / remainingPercentage 80, name and resetAt
  preserved).
- `go build ./...` and `go vet` on the touched package clean. No frontend
  files change; the svelte-check totals are unchanged.
- Live proof with a real MiniMax account remains open by environment, not
  by code: no MiniMax connection exists in any database reachable from
  here, so the first staging read of a locked-then-refilled MiniMax
  account is the live check. The probe covers the decision logic; the
  fetch itself is the pre-existing port, unchanged except for the window
  payload shape, which the probe pins to upstream's.
