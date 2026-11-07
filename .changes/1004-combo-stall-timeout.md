# 1004 — Combo response timeout is now a stall timer: only total upstream silence is cut

## Symptom

A combo "auto" attempt (antigravity / gemini-3.8-flash, ~104k-token prompt
after compression) received its upstream response headers at 118.5s — the
upstream was alive, just slow to first token on a huge prompt — but had
committed no output when the combo's 120-second response timeout fired.
The attempt was canceled, recorded as a 499 "context canceled" attempt
row, and the combo fell back. For the owner's workload (coding: long
streams, contexts above 100k tokens, slow first tokens are normal) this
kept sacrificing healthy attempts at an arbitrary wall-clock line.

## Root cause

`comboResponseTimeout` ran one absolute deadline from attempt start
(`time.AfterFunc(timeout, expire)`). It knew only two states — committed
and not — and had no notion of upstream progress: response headers, body
chunks read while translation buffered them, and a genuinely dead
upstream were indistinguishable to the timer, so all three died at the
same instant.

## Fix

- `FirstTokenTimeoutMs` is now the maximum tolerated upstream SILENCE.
  The timer starts at attempt start and restarts in full on every
  upstream progress signal; it still cancels only an uncommitted
  candidate, and once output commits it is disarmed exactly as before.
  The setting name, `comboTimeoutPolicy`, the grace/benching semantics,
  and the last-candidate exemption are unchanged — only the meaning of
  the window moved from "time since start" to "time since last sign of
  life".
- The gate stores its timeout and learns `Progress()` (mutex-guarded
  `timer.Reset`, a no-op once committed, and deliberately unable to
  resurrect an expired gate). The gate registers itself in its own
  derived context under an unexported key; the new `noteUpstreamProgress`
  helper pings whatever gate a context carries and no-ops otherwise, so
  direct (non-combo) requests are completely unaffected.
- Progress call sites, one ping per event: the Gemini-native forwarder
  when the upstream response headers arrive and on every chunk the
  streaming loop reads (including chunks translation buffers before any
  client write); the generic forwarder when the upstream response
  arrives and on every chunk read in its stream handlers (Claude-upstream
  translation, passthrough, and OpenAI→Claude translation).
- Copy follows the semantics: the `FirstTokenTimeoutMs` field comment
  and the combo editor's shared help text (`RESPONSE_TIMEOUT_HELP`) now
  describe a silence window — "any sign of life restarts the clock" —
  instead of a first-response deadline.

## Verification

- `go build ./...` green; `go vet` clean on `internal/handlers/chat` and
  `internal/db`; edited files gofmt-clean.
- A temporary probe (`TestProbeStallTimer`, run once and deleted)
  asserted the new semantics directly: an 80ms gate pinged every 30ms
  five times (~150ms total) stayed alive and its Write committed; a
  silent 60ms gate expired and a late ping did not resurrect it; the
  helper no-ops on a gate-less context.
- Web build green; svelte-check at the standing baseline (89 errors /
  56 warnings, unchanged).
