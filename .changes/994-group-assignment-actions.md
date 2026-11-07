# 994 — Proxy Pools: add-to-group actions (row, bulk, form)

## Symptom

There was no way to put an existing pool into a group except editing it
one by one and retyping the group name into a free-text field (owner
report, 2026-11-07) — and no bulk assignment at all. Groups are string
tags (owner decision: keep the tag model, build the management UX), so
every missing action was a group-field write the UI never offered.

## Fix

Frontend only:

- New `proxypools/GroupPickerModal.svelte`: lists existing groups with
  member counts, offers "No group" when removal means something, and a
  new-group name field — one picker for every assignment surface.
- Pools list: a per-row "Set group" action opens the picker for that pool.
- Bulk bar: a "Set Group" action applies the picker to the whole
  selection; writes run per pool with an honest ok/failed report (the
  existing bulk-report discipline), never claiming more than the server
  confirmed.
- The pool form's Group field becomes a combobox (native datalist over
  the existing groups): free text still allowed, existing names offered,
  which kills the typo-splits-group failure mode.

## Verification

- svelte-check baseline held (89/56, zero new); web build succeeds.
