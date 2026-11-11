title: Claude-wire streams: estimate stands in when upstream reports input_tokens 0
version: v1.10.42

## Fixed
- The prompt_tokens estimate shipped in v1.10.40/v1.10.41 covered the
  OpenAI-wire relays, but MiniMax (minimax-code / minimax-code-global)
  is a Claude-wire provider: its streams are translated event by event
  by the Claude-to-OpenAI translator, which took prompt_tokens solely
  from message_start's input_tokens — and MiniMax's mavis gateway
  reports input_tokens: 0 there and never revises it, so the terminal
  chunk still reached clients with prompt_tokens: 0 and Hermes context
  meters stayed at 0%. The translator now (a) adopts a positive
  input_tokens reported in message_delta (the cumulative usage some
  gateways only send at the end), and (b) substitutes the gateway's
  body-size prompt estimate at the terminal chunk when the count is
  still zero, updating the recorded usage to match. All three stream
  state construction sites (executor Claude pump, chat handler Claude
  stream, forced-SSE fold) seed the estimate from the request context.
