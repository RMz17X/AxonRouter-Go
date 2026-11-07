# 984 — Capacity 503s no longer borrow quota reset durations (cooldown bounded to the backoff class)

## Symptom

On the JS router (production, 2026-11-07), one account's 503 "no
capacity available for model" benched the model fleet-wide for ~2
hours: the capacity writer copied an hours-long lock duration that
followed an upstream reset hint, instead of the 60–120s window a
capacity signal is supposed to earn, and no successful dial cleared
it. Fixed there in v0.1.34 by clamping the capacity writer.

The Go router had the same defect in a different shape. For every
non-429 retryable status, `retryableCooldownSec` replaced the
classifier's backoff with a reset duration extracted from the error
body (`quotaResetDelay` metadata or "resets in …" text), capped only
at 72h. A 503 is a capacity/node verdict, but a reset duration found
in its body describes a different window (a quota reset) — so one
capacity response could park an account (the fallback handler writes
an account-level rate-limit lock for non-model-scoped errors) for
hours, far beyond the capacity class's own 2s→5min backoff.

## Fix (internal/handlers/chat/retry_after.go)

- `retryableCooldownSec` no longer consults `extractResetDuration`
  for status 503: a 503 cooldown is always the classifier's backoff
  (Backoff class, 2s doubling capped at 5min), the same short window
  the JS fix clamps to. `cooldownSecForClassification` is the single
  entry point for both failover loops (fallback and combo), so one
  change covers both.
- Scope is deliberately narrow: 429 handling (Retry-After / RetryInfo
  honoured in both directions) and the reset-duration reading for
  every other status are unchanged, as are the model-scoped quota
  paths (a 503 whose body carries QUOTA_EXHAUSTED /
  MODEL_CAPACITY_EXHAUSTED markers still routes model-scoped before
  this function is reached) and the banned/exhausted unclamped
  classes.
- Known remaining difference from the JS fix: JS also clears its
  provider-wide model bench on a successful dial. Go's locks are
  per-account + per-model rather than provider-wide, and with the
  duration bounded to the backoff they expire in minutes, so no
  success-clearing path is added here.

## Verification

- Temporary in-package probe (run, passed, deleted):
  cooldownSecForClassification with synthetic upstream errors —
  503 + body carrying quotaResetDelay 7200s → 2s (was 7200s before
  the fix); 503 plain overloaded body → 2s; 503 at the backoff cap →
  300s; 429 + Retry-After 7200 header → 7200 (unchanged); 403 + the
  same reset body → 7200 (unchanged, scope discipline).
- `go build ./...` and `go vet` on the touched package: clean.
