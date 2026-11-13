title: Prompt-total credibility floor applied at every usage egress
version: v1.10.48

## Fixed
- v1.10.47 floored the Claude translator's terminal chunk, but the
  segment-count under-report still reached clients and the request
  ledger through the other usage egresses: the executor's and chat
  handler's per-chunk body patcher only replaced an exact zero, the
  raw-stream usage capture substituted only on zero, and the Claude
  stream accumulator recorded mavis's start-frame segment count
  verbatim (fleet proof after the .47 deploy: identical MiniMax
  turns closed with prompt_tokens 474, 1,634 and 796 against
  ~200K-token forwarded prompts, and one probe in three still
  returned prompt_tokens 9). One rule now applies everywhere, via
  translator.FloorPromptTokens / PatchPromptUsageFloor (the renamed
  PatchZeroPromptUsage): a prompt total under a quarter of the
  gateway's body-size estimate cannot describe the forwarded
  request, so the estimate stands in — on the translated terminal
  chunk, on both pumps' wire patchers, on non-stream bodies
  (including translated Claude responses), in the usage capture,
  and at every accumulator that feeds the recorded usage. Credible
  totals are untouched: honest reporters, mavis on a cold cache
  included, report over half the estimate.
