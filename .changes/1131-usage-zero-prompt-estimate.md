title: Usage: estimate stands in when upstream reports prompt_tokens 0
version: v1.10.40

## Fixed
- Providers that always report prompt_tokens: 0 in their usage (MiniMax
  streaming is the proven case) left every OpenAI client's context meter
  stuck at 0% and recorded zero input tokens for the whole session,
  because the passthrough relay forwarded the zero verbatim. The chat
  passthrough (streaming and non-streaming) and the executor's JSON
  response path now substitute the gateway's existing body-size prompt
  estimate — (request bytes + 3) / 4, the same fallback the translated
  stream's terminal usage block already applies — whenever a response
  usage block reports prompt_tokens as 0 or omits it, and fill
  total_tokens as estimate + completion when the upstream left it 0 or
  absent. Real reported numbers are never overwritten, and the usage
  recorded for the request log now matches what the client receives.
