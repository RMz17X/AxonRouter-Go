title: Usage estimate on the executor stream path; smart combo save keeps arranged order; combo modal closes only via buttons
version: v1.10.41

## Fixed
- v1.10.40's prompt_tokens estimate never reached the wire for
  executor-served providers: their streams are relayed by the executor's
  SSECopy pump, not the chat handler's passthrough, so a MiniMax stream
  still arrived with prompt_tokens: 0 and client context meters stayed
  at 0%. The estimate patcher now also runs in the executor's stream
  pump, and the executor's usage capture substitutes the same estimate
  so the recorded usage matches what the client was sent.
- Editing a smart combo silently discarded model reordering: drag and
  move buttons write the visible model list, but the save rebuilt the
  list from the tier pools (easy block, then hard block, then the
  rest), and the pools only ever change on tier toggles — so the saved
  order snapped back on every save. The visible list now decides the
  saved order, and each pool is saved in that same order filtered by
  tier; a model added to the combo joins the saved list where the
  operator left it instead of being regrouped.
- The create/edit combo modal closed on backdrop click and on Escape,
  losing in-progress edits to a stray tap. It now closes only through
  its buttons (close, Cancel, Save): the shared Modal gains a
  closeOnEscape opt-out, and the combo modal disables both backdrop
  and Escape dismissal.
