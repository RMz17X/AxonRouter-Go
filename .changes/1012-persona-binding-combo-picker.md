# 1012 — Persona binding target reuses the Combos model picker

## Symptom

Fragment 1011 gave the Persona binding editor a target picker, but it was a
persona-local native `<select>` — a second, poorer picker next to the one
the Combos flow already has. The owner asked for the Combos picker itself:
same UI, reused, not a parallel implementation ("pick model ui nya kaya di
pick model combo, reuse itu aja").

## Root cause

Fragment 1011 built `BindingTargetPicker.svelte` from the Playground
inventory instead of reusing `web/src/components/combos/ModelPickerModal.svelte`
— the searchable, grouped pill picker Create/Edit Combo already opens.

## Fix

- `BindModal.svelte`: the native select is replaced by a display field plus
  (add mode only) a "Pick model…" button that opens `ModelPickerModal`
  as-is — `target="combo"` (the value whose `resolveFilteredCombos` keeps
  combos pickable as entries), `connections` from `api.getConnections()`,
  `combos` from `api.getCombos()`, fetched on mount exactly as CombosView
  is fed. Single-select semantics over the multi-select picker: a pick
  sets the target and closes; `addedModelValues` carries the current
  target so the existing pick shows as added. No prop the modal lacks was
  needed, and the modal itself is untouched.
- The display field shows the target string with the MODEL/COMBO chip
  BindingList's rows use (combo membership derived from the same combos
  fetch; a failed fetch just leaves the chip reading MODEL). Edit mode
  shows value + chip only — the target stays immutable, no picker.
- Not-in-inventory preservation without machinery: the target is plain
  state plus display, written only by the initial edit capture and by an
  explicit pick. Opening the picker cannot clear or rewrite it, so a
  target whose connection was deactivated, whose combo was deleted, or
  that is a legacy catalog id still displays and saves unchanged.
- Copy follows the picking flow: empty state "No model picked yet", the
  intro says to pick the model or combo to bind, and the existing
  empty-target validation ("Pick a model or combo to bind.") still blocks
  saving. The keyword-coverage hint keeps working off the target value.
- `web/src/components/persona/BindingTargetPicker.svelte` is deleted —
  superseded by the reuse above. BindingList, BulkArm, and TestMatchModal
  are unchanged. No API or backend change.

## Verification

- Web build via the heavy gate: green.
- svelte-check (`--tsconfig ./tsconfig.app.json`): 89 errors / 56 warnings —
  exactly the standing baseline.
- `git status` shows the diff limited to `BindModal.svelte`, the deleted
  `BindingTargetPicker.svelte`, and this fragment; zero `.go` changes.
