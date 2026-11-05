## MiniMax Code: a Claude client's reply is no longer translated into OpenAI shape

**Symptom.** A /v1/messages client routed to minimax-code received an
OpenAI-shaped body (no `type: "message"`, no Claude content blocks) in
answer to a Claude Messages reply from the mavis upstream —
TestMiniMaxCodeClaudeWire in the integration suite.

**Root cause.** A flag-polarity collision. The Claude response helpers
read TranslateResp in their convention (true = the client speaks
Claude → pass through; false = OpenAI client → translate). The chat
layer's translateResponse has the opposite meaning on the /v1/messages
lane: it is false for Claude-native providers precisely because no
translation was expected. ForwardMinimaxCode used to ignore the flag
for Claude clients (raw relay); a later change routed every reply
through the helpers, so the messages lane's false was obeyed as
"translate to OpenAI" and Claude clients got chat.completion JSON.

**Fix.** ForwardMinimaxCode hands the helpers the flag in their own
convention: `req.TranslateResp = req.ClaudeClient` before dispatch.
ClaudeClient is derived from the endpoint itself at both
request-construction sites, so it cannot drift from the client's
actual format. The /v1/chat/completions lane keeps the translation
the earlier change was for; the /v1/messages lane passes through —
now through the helpers, so tool decloaking applies there too (the
old raw relay skipped it).

**Verification.** TestMiniMaxCodeClaudeWire (and the global-site and
auth-refusal companions) pass against a live PostgreSQL through the
real router; previously the wire test failed on the reply shape.
