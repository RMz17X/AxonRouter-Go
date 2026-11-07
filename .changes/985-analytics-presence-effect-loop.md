# 985 — Usage Overview presence effect loops forever (effect_update_depth_exceeded) while requests are in flight

## Symptom

Opening the Usage Overview while the gateway was serving traffic
threw an uncaught `effect_update_depth_exceeded` (Svelte) and the
view died. With no request in flight the page loaded fine, which is
why it survived staging checks that ran against an idle gateway.

## Root cause

The topology presence effect in AnalyticsView.svelte stamps
providers from `Date.now()` (active requests, pulses) and then, in
the same effect, merges them into the `observedActivity` state —
reading it (`new Map(observedActivity)`) and writing it back
(`observedActivity = next`). The read subscribes the effect to the
very state it writes, and the merge's "only strictly newer stamps"
guard can never stop the cycle: the stamp source is the current
time, so every pass produces a newer stamp, `changed` is always
true, the write re-triggers the effect, and Svelte aborts after the
update-depth limit. The errorProvider leg of the same effect had
already been fixed for this exact pattern (stamp only on change,
review cycle 3 A-1); the active-request and pulse legs kept the
stamp-with-now behaviour.

## Fix (web/src/components/analytics/AnalyticsView.svelte)

- The merge block now runs inside `untrack(...)`: growing
  `observedActivity` from its previous value is bookkeeping, not a
  reactive dependency. The effect re-runs on its real inputs only
  (activeRequests, pulseProvider, errorProvider, stats recent rows),
  the stamps still merge monotonically, and the five-minute presence
  expiry (driven by the separate topologyNow clock and the
  topologyProviders derivation) is unchanged.

## Verification

- The loop is provable from the code alone: tracked read + write of
  one $state in a single effect whose guard compares against an
  always-advancing clock. The fix removes the subscription, so the
  cycle cannot form; presence semantics (who is stamped, when they
  expire) are byte-for-byte the same inputs and outputs.
- svelte-check: 89 errors / 56 warnings before and after; the
  normalized error set is identical except four pre-existing
  AnalyticsView errors at line numbers shifted by the insertion.
  Zero new errors.
- `bun run build` succeeds (bundle + pre-compression complete).
