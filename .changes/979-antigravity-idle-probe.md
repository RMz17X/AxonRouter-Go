# 979 — Scheduled generation probe for idle Antigravity accounts

## Problem

Fragment 973's ToS-disable detection is traffic-driven: the chat lane
classifies generation responses as they pass. An account Google
disabled while it sat idle produces no generation responses, so the
disable stays invisible until the account's next real request draws the
verdict — mid-rotation, in front of a user.

## What was built

- `internal/schedule/idleprobe.go` — the prober. On a 1-minute tick
  (first sweep 15s after boot) it lists Antigravity connections and
  probes each due one with ONE tiny generation request through the
  existing in-process probe lane, `ChatHandler.PingConnection`
  (connection pinned, probe context) — the same lane the quota
  auto-ping uses, i.e. real credential resolution, OAuth refresh,
  proxy-pool binding and executor. No HTTP self-call and no new probe
  implementation: the test-model route's decisive surface IS the
  generation path, and this is it, in-process. The loadCodeAssist
  connection probe is deliberately NOT used (it answers for terminated
  accounts).
- The response flows through the chat lane's existing classification
  untouched: a ToS 403 disables the account exactly as live traffic
  would (973), a 429 parks it as live traffic would, a transport
  failure changes nothing. Fail-open: a failed probe is logged and
  dropped; the prober itself writes no account state, ever.
- Probe shape: model `gemini-3.8-flash-low` (cheapest broadly-entitled
  flash lane; the disable verdict is account-shaped, not model-shaped),
  prompt "hi", max_tokens 1 — the auto-ping body shape.
- Guardrails: concurrency 1 (sequential sweep); due list shuffled with
  a jittered 3–13s pause between probes; at most 8 probes per tick;
  per-account minimum interval enforced from an in-memory last-probe
  map (an attempt counts, so a dead-egress account is not re-probed
  every tick). Disabled accounts are never dialed: a connection is
  skipped when it is inactive OR carries the stored disabled verdict
  (data.status / data.testStatus == "disabled", the
  DisableConnectionWithReason marker) — the second check covers the
  resurrection shape (marker intact on an active row) where isActive
  alone would let a probe through. Idle is measured from the
  connection's lastUsedAt stamp (creation time for never-used rows).
- `internal/app/server.go` — `startIdleProbe` wired beside the
  auto-ping scheduler, bound to the shutdown context.

## Settings (one home)

All in `providerStrategies.<provider>` (`internal/db/settings.go`),
resolved per sweep by `Repo.IdleProbeConfigFor`
(`internal/db/accounts_idleprobe.go`), following fragment 974's
derived-default pattern:

- `idleProbeEnabled` — unset = derived default (enabled for
  antigravity, off elsewhere); explicit false turns the prober off.
- `idleProbeThresholdSec` — unset or non-positive = 6 hours.
- `idleProbeIntervalSec` — unset or non-positive = 1 hour.

Only antigravity is in the prober's scope even when another provider's
strategy enables it: the disable blindness being closed is
Antigravity's, and no other provider's verdicts feed a disable path.

## Quota cost, honestly

Each probe is one real generation call against the account's own quota
("hi", one output token, cheapest flash lane). With the defaults, a
permanently idle account is probed ~4 times a day; an account with any
real traffic is probed rarely or never, because traffic keeps
resetting its idle clock. This is a deliberate trade: a handful of
one-token generations per idle account per day, in exchange for
learning about a ToS disable on a background tick instead of from a
failed user request.

## Found and fixed on the way (latent bug, shipped code)

`applyProviderStrategies` (`internal/db/settings_parse_maps.go`)
rebuilds every ProviderStrategy field-by-field and never carried the
pointer knobs — fragment 974's `cooldownJitterSec` was silently dropped
on the parse path, so its EXPLICIT setting has been dead since v1.9.20
(only the derived default ever applied). The parse now carries
`cooldownJitterSec` plus the three new idle-probe fields verbatim
(presence-checked, since explicit 0/false is a decision for these
knobs). Without this fix the settings above could not have been read
at all.

## Verification (boot smoke, staging PG, scratch DB — dropped after)

Seeded three Antigravity connections (fake credentials): A = active,
idle 24h; B = active but carrying the stored disabled marker; C =
inactive, idle 24h.

- Phase A, `idleProbeThresholdSec=999999999` in providerStrategies:
  two sweeps, ZERO probe/skip lines — the settings home governs.
- Phase B, threshold flipped to 60 (settings row only, no restart):
  the next tick logged `skipping stored-disabled connection` for B and
  `probing idle connection` for A — and nothing for C. B's row was
  byte-unchanged afterwards (updatedAt still the seed instant, no
  lastError): never dialed. A's probe travelled the lane and its
  existing classification disabled the account (the fake credential
  resolves to no project ID — the lane's own disable verdict, recorded
  with lastError), proving end-to-end that a probe verdict lands in
  the same disable path as live traffic. With real credentials the
  same dispatch draws the upstream's actual generation verdict.
- `go build ./...` clean; `go vet` clean on internal/db,
  internal/schedule, internal/app.
