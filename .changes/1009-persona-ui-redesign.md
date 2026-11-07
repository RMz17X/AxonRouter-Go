# Persona page UI redesign — segmented workspaces, preset cards, Markdown prompt editor (1009)

## Symptom

The Persona page (tab id `jailbreak`) was a single ~2,265-line component
rendering every workspace as one endless scroll: the preset library, the
preset editor, keyword rules, chat-turn scripts, the per-model bindings,
bulk arming, test-match, and the global exclude list all stacked on one
page. Presets rendered as dense table rows with no prompt preview, the
page carried three summary cards plus two full list cards before the
operator reached the control they came for, and nothing about the layout
distinguished the two distinct jobs the page does — writing prompts and
arming them.

## Root cause

The page grew feature by feature (#1–#8 additions: import, bulk arm,
test-match, payload preview, usage counters, batch selection…) and each
one appended its state, handlers, and markup to the same file, so the
structure never caught up with the feature set.

## Fix

UI/UX only — no backend or API changes; every call, payload, and
validation is byte-identical to before.

- **Split** `web/src/components/JailbreakView.svelte` (2,265 lines) into a
  252-line composing view plus `web/src/components/persona/`: `PresetGrid`,
  `PresetCard`, `PresetEditor`, `PromptField`, `TurnEditor`,
  `PresetImport`, `BindingList`, `BindingBatchBar`, `BindModal`,
  `BulkArm`, `TestMatchModal`, `GlobalExcludes`, and `types.ts` (form
  state). `KeywordRules` and `Pagination` are reused as-is.
- **Structure**: in-page segmented tabs **[Presets | Bindings]** replace
  the stacked scroll. A slim header strip carries the page title, a
  one-line description, and the live counts (presets, bindings armed,
  injections); the active segment's primary action (New Preset / Add
  Binding, plus Test match) sits in the segment bar. The global exclude
  list lives under Bindings, which it governs; the "How it works"
  reference stays collapsed at the bottom.
- **Presets segment**: presets are cards in a responsive grid (1 column
  on the phone, 2–3 on wide screens) — name, a 2–3 line monospace prompt
  excerpt, chips for kind, mode (Single/Chat), position (Prepend/Append),
  Gemini safety state, hit count, and Edit/Duplicate/Delete. Filter,
  sort, page size, batch select/export/delete, and pagination are
  unchanged; an empty library now gets a proper empty state with a
  New Preset call to action.
- **Bindings segment**: compact rows — a labelled grid (Target / Preset /
  Inject) on wide screens, stacked cards on the phone. Each row shows a
  MODEL/COMBO type chip (combo names come from the existing combos API),
  the bound preset name, the enable Toggle, and Edit/Unbind. Batch
  rebind/enable/disable/unbind, filters, and pagination are unchanged;
  the add/edit binding modal (Single free-text id + Bulk catalog arm)
  keeps every behaviour, including the keyword-coverage hint.
- **Editor**: still modal-first. The Write form is organised into
  labelled sections — **Prompt** (id, name, injection mode, position,
  prompt), **Behavior** (source, kind, Gemini safety), **Keywords**
  (include rules, excludes). The prompt field is now a **Markdown
  editor**: a helper line shows the Markdown structure, and the existing
  rendered Preview (via `marked`) sits beside it; the prompt is stored
  and injected verbatim as Markdown, exactly as before. Chat-mode turn
  editor, file/URL import tabs with their log, and the test-match modal
  with payload preview all survive in their own components.

## Verification

- Web build via the heavy gate (`tsc -b && vite build && precompress`):
  green.
- `svelte-check --tsconfig ./tsconfig.app.json`: exactly the standing
  baseline, 89 errors / 56 warnings — no new diagnostics, none in the
  new files.
- `git diff` contains zero `.go` files and zero changes outside
  `web/src/components/JailbreakView.svelte`, the new
  `web/src/components/persona/` directory, and this fragment.
- Capability pass against the pre-redesign file: preset CRUD, duplicate,
  batch export/delete, six sort orders, filters + pagination on both
  lists, file upload + GitHub/URL import with import log, keyword rules,
  per-preset excludes, global exclude list, chat-turn scripts, Gemini
  safety toggle, preset `order` round-trip, binding CRUD + enable
  toggle, batch rebind/enable/disable/unbind, bulk arm with substring
  and RE2 filters, test-match, and payload preview — each mapped to its
  new component in the handoff report.
