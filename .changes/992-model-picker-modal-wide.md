# 992 — The model picker modal takes the desktop width upstream gives it

## Symptom

The owner asked for the "Add Model to Combo" modal (the shared model
picker) to be wider on desktop. The Go port capped the panel at
`max-w-md` (448px), so on a desktop the provider groups and model pills
crammed into a phone-width column with heavy scrolling.

## Root cause

The width was a porting shrink: upstream's ModelSelectModal renders at
`w-[min(96vw,1180px)]` — nearly three times wider. The Go content itself
(pill groups in wrapping flex rows) already fills whatever width it is
given; only the panel cap and the list's fixed 400px height kept it
small.

## Fix

Frontend only (`ModelPickerModal.svelte`):

- The panel cap goes from `max-w-md` to `max-w-[1180px]`, upstream's
  width. The overlay's existing padding keeps the same inset on phones,
  where the panel stays full-width as before.
- The results list drops its fixed `max-h-[400px]` for `flex-1 min-h-0`
  inside the panel's existing flex column, so the list uses the panel's
  full 85vh height instead of stopping at 400px while the panel around
  it sits empty.

This modal serves every picker target (combo, vision, audio, judge,
router) — all of them get the same width, as upstream's single modal
does.

## Verification

- svelte-check (with `--tsconfig ./tsconfig.app.json`) totals identical
  to the pristine baseline (89 errors / 56 warnings, zero new) and the
  web build succeeds.
- Pixel proof rides on the next staging view, like 988–991.
