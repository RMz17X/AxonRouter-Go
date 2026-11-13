title: Claude streamed turns: record the real prompt-cache usage
version: v1.10.47

## Fixed
- A streamed Claude-native turn reported its counters in two frames:
  `message_start` carries the input side (`input_tokens`,
  `cache_read_input_tokens`, `cache_creation_input_tokens`) under
  `message.usage`, and `message_delta` carries the output side
  (`output_tokens`) at the top level. The passthrough lanes read usage
  one frame at a time with `ParseClaudeUsage`, which only understands a
  top-level `usage` object — so neither frame was ever complete: the
  prompt count lost the cache and the output count lost the prompt.
  Every streamed Claude turn was logged with `cached_tokens = 0` and a
  prompt count that fell back to the character estimate, which made a
  warm prompt cache read as a cold one in the usage history and billed
  the turn at the full input rate.
- `translator.AccumulateClaudeStreamUsage` now folds the two frames into
  one running record and returns a NEW pointer on a recognised event, so
  a caller can tell "this frame changed the usage" from a no-op by
  identity and a `message_delta` never clobbers the prompt and cache
  figures `message_start` established. The folded record's
  `prompt_tokens` is cache-INCLUSIVE (`input + cache_read +
  cache_creation`), matching the canonical OpenAI convention the pricing
  table assumes.
- Wired into every streamed Claude path: the Claude-client passthrough
  (`handleClaudeMessagesStream`), the wire-OpenAI relay served by a
  Claude-native upstream (`sseStream`'s decloaker branch and its
  `SSECopy` branch), and the chat handler's passthrough. The accumulator
  runs BEFORE the generic parser, so a `message_delta` — which carries a
  top-level `usage` object and would otherwise be read as a whole usage
  record with `prompt_tokens = 0` — cannot wipe the cache figures.
