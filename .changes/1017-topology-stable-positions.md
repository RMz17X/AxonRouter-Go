# 1017 — Usage Overview topology nodes keep their positions

## Symptom

On /dashboard/usage, provider nodes on the topology map changed position
constantly: a provider arriving, going quiet, or merely moving in the
activity order reshuffled every node on the ring, so the map could not be
read at a glance and the operator's pan jumped on every membership change.

## Root cause

`ProviderTopologyCard.svelte` placed nodes purely by list position: the
angle was `-π/2 + 2π·idx/s` with `s = providers.length`, and the ring
radii were functions of the same `s` (`rx = max(320, 204·s/2π)`,
`ry = max(200, 0.55·rx)`). The `providers` prop is activity-ordered, so
any membership or order change altered every angle and the ring size at
once. The view layer compounded it: the card refit (resetting pan) on
every node-count change.

## Fix

- New pure module `web/src/components/analytics/topologyLayout.ts`: a
  session slot allocator plus ring geometry. Each provider owns a
  numbered slot keyed by its id (the same lowercased identity the card
  already matches activity against):
  - a present provider keeps its slot unconditionally;
  - a provider that leaves frees its slot for newcomers, but the slot is
    remembered — on return it reclaims the same seat while it is free
    (a slot another present provider owns is never taken back);
  - a newcomer takes the lowest free slot;
  - ring capacity comes from a coarse tier ladder (6/12/18/24/36/48/64)
    covering the highest slot in use, and never shrinks during the
    session, so radii and angles are stable; a tier step forced by a
    genuinely larger concurrent set is the only moment existing angles
    may change.
  Angle and radii derive from slot and capacity only, using the card's
  existing formulas with capacity in place of the live count.
- `ProviderTopologyCard.svelte` consumes the allocator: placement is
  keyed by provider id, the prop order no longer affects it, and the
  refit effect now fires only when the ring size itself changes (a tier
  step), so arrivals and departures no longer reset the operator's pan.
  Hub, edges, artwork, active/pulse/last/error highlighting, drag/pan,
  zoom controls, and the empty state are unchanged.
- `AnalyticsView.svelte` is untouched: its derivation decides who is on
  the map (the 10-minute retention of fragment 1016), the card now
  decides where they sit.
- Known boundary: slot memory lives in the card's component state, so
  it lasts while the card stays mounted; leaving the Usage view and
  coming back starts a fresh assignment (in the same first-seen order
  the old layout used).

## Verification

- Web build via the heavy gate: green.
- svelte-check (`--tsconfig ./tsconfig.app.json`): baseline
  89 errors / 56 warnings, unchanged.
- Allocator exercised directly (scratch script, since removed) against
  the shipped module: A, B, C placed at capacity 6 → B leaves, A and C
  coordinates bit-identical → B returns, reclaims its slot exactly →
  newcomer D takes B's freed slot while A and C stay put → list reorder
  moves nobody → a 7th concurrent provider steps capacity to 12 (the
  one allowed movement) and later departures never shrink it, survivors
  keeping their post-growth seats.
