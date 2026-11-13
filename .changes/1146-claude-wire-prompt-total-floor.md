title: Claude-wire prompt total floored at the body-size estimate when upstream under-reports
version: v1.10.47

## Fixed
- Clients of Claude-wire providers (MiniMax Code) still saw a ~0%
  context meter on most smart-combo turns even after the zero-usage
  fix: mavis reports input_tokens 0 in message_start, and the
  message_delta figure adopted later counts only the newest segment
  of the conversation, so a turn that forwarded a several-hundred-
  thousand-token prompt closed with prompt_tokens in the hundreds
  (staging census 2026-11-13: 3,643 of 4,616 MiniMax-served `auto`
  requests with ≥30K-token prompts ended with prompt_tokens < 3,000;
  antigravity-served turns were unaffected). terminalUsage now
  substitutes the gateway's body-size prompt estimate whenever the
  reported total is less than half that estimate — a total that low
  cannot describe the forwarded body — while honest reporters, whose
  totals track the body within tokenizer variance, keep their own
  number. The recorded usage matches the corrected wire value, as
  before.
