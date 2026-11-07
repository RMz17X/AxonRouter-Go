# 1013 — Persona presets gain a third injection position: replace

## Symptom

A Persona preset could only add its prompt around the client's own system
prompt — prepended before it or appended after it. There was no way to make
the persona prompt the *only* system content: a client that ships its own
strong system prompt kept it in force next to the persona's, and the two
could fight. The owner asked for the missing third option ("gak ada sistem
replace?") after comparing prepend and append in the preset editor.

## Root cause

`injectSystemPrompt` (`internal/handlers/chat/jailbreak.go`) normalized
every position other than `"append"` to `"prepend"`, so a stored
`"replace"` could never reach the injection logic — and the preset
editor's form loader coerced anything that was not `"append"` back to
`"prepend"` as well.

## Fix

- `injectSystemPrompt` accepts `"replace"` as a distinct position
  (normalization unchanged otherwise: only exact `"append"`/`"replace"`
  are special, everything else still falls back to `"prepend"`). For
  replace, the client's own system content is discarded per wire shape:
  - Claude top-level `system`: a string becomes the persona prompt alone;
    a block array becomes a single-element array holding the persona block.
  - Gemini `system_instruction`: `parts` becomes a single-element array
    holding the persona part.
  - OpenAI `messages`: every system/developer message is removed and the
    persona system message is inserted at the index of the first removed
    one (index 0 when there were none), so the remaining messages keep
    their relative order.
  The existing idempotency guard (prompt already present in a system slot
  → no-op) runs before the replace branch in every shape, exactly as it
  does for prepend/append. A body with no `messages` at all already fell
  back to a lone persona system message, which is replace by construction.
  Prepend/append behaviour, `prependSystemPrompt`, and turns mode
  (`injectChatTurns`) are untouched.
- No other Go site needed extending: the preset save handler passes the
  doc through without validating position, the DB stores the doc
  generically as JSON (position only defaults to `"prepend"` when empty —
  its field comment now names all three values), and the playground passes
  a chosen preset's stored doc through verbatim (its ad-hoc session prompt
  stays hardcoded to prepend by design).
- Web: the position radio group in `PromptField.svelte` gains
  "Replace (discard the client's system prompt)"; the `JailbreakDoc`
  position type in `client.ts` and the `PresetFormState` type in
  `types.ts` widen to `'prepend' | 'append' | 'replace'`, and
  `presetToForm` no longer coerces a stored `replace` back to `prepend`;
  `PresetCard.svelte` renders a red "Replace" chip next to the existing
  blue "Append" and neutral "Prepend" chips.
- Noted, deliberately unchanged: the dashboard payload preview
  (`HandlePreviewJailbreak`) builds its sample messages with a hardcoded
  prepend for every preset — it never simulated append either. Making it
  faithful means routing the preview through the chat package's
  `injectSystemPrompt` instead of a second, drifting copy of the logic,
  which is a separate change.

## Verification

- `go build ./...` and `go vet` on `internal/handlers/chat` via the heavy
  gate: green.
- A temporary in-package probe (written for this check, run once, deleted)
  exercised replace across all three wire shapes: Claude string and block
  array collapse to the persona alone, Gemini parts collapse to the persona
  part, a messages array with mid-list system + developer messages keeps
  the surviving messages in order with the persona message at the first
  removed slot, a system-free array gets it at index 0, the idempotency
  guard still no-ops, and an unknown position still falls back to prepend.
  All cases passed.
- Web build via the heavy gate: green.
- svelte-check (`--tsconfig ./tsconfig.app.json`): 89 errors / 56 warnings,
  exactly the standing baseline.
- `git status` limited to `internal/handlers/chat/jailbreak.go`,
  `internal/db/jailbreak.go` (field comment only), `PromptField.svelte`,
  `PresetCard.svelte`, `types.ts`, `client.ts`, and this fragment.
