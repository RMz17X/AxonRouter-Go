# 1011 — Persona bindings pick their target from the live inventory

## Symptom

The Persona binding editor (fragment 1009's BindModal) took its target as a
free-text field ("providerx/deepseek-4-pro, or a combo name"). A binding is
meant to be model-based, but nothing stopped a typo'd or invented id: the
binding saved happily and then never matched a real request.

## Root cause

The binding form predates the live model inventory. The Playground feature
(fragment 1008) added `GET /api/playground/models` — active connections'
models plus combos — but the binding editor never adopted it as its target
source.

## Fix

- New `web/src/components/persona/BindingTargetPicker.svelte`: a grouped
  native `<select>` fed by `api.getPlaygroundModels()`. One `<optgroup>` per
  provider/connection for model entries (labelled with provider +
  connection name), and a final "Combos" optgroup. The selected value is
  the entry's `model` string — the wire id for a model, the name for a
  combo — exactly what `PUT /api/jailbreak/models {id, presetId, enabled}`
  already stores. No API or backend change.
- `BindModal.svelte`: the free-text target input is replaced by the picker.
  Copy follows: the field is "Model or combo", the intro says to pick, and
  the empty-target validation now reads "Pick a model or combo to bind."
  The keyword-coverage hint keeps working off the picked value.
- Preservation: a binding whose target is no longer in the inventory (a
  model on a since-deactivated connection, a deleted combo, a legacy
  catalog id) keeps its target as a pinned first option labelled
  "(current) <id>", so opening the editor to change only the preset or the
  on/off switch can never silently rewrite or lose the target. The picker
  is disabled in edit mode, as the text field was.
- Loading, failure, and empty states are stated in the modal: "Loading
  models…" while the inventory loads, the load error if it fails, and —
  when there are no active connections and no combos — a note that a
  binding cannot be created until one exists, instead of a dead control.
- Out of scope, unchanged: the Bulk tab's catalog substring/regex arming,
  TestMatchModal, and the binding list's batch operations.

## Verification

- Web build via the heavy gate: green.
- svelte-check at the standing baseline (89 errors / 56 warnings).
- Diff is limited to BindModal.svelte, the new BindingTargetPicker.svelte,
  and this fragment; zero Go changes.
