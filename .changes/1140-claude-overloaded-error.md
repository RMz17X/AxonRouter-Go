### Fix: mid-stream aborts reach Claude clients as `overloaded_error`

- A mid-stream abort — an upstream cut or a stall watchdog firing — reached Claude-format clients as an SSE error event of type `server_error`. The frame carries no HTTP status, and Claude Code retries a streamed error event ONLY when its type is `overloaded_error` (the type Anthropic itself uses for transient failures); `server_error` is read as fatal, so every upstream cut ended the run without a retry.
- The Claude frame now uses `overloaded_error`. The human-readable message and the machine code (`upstream_error`, `gateway_timeout`, `request_aborted`, `internal_server_error`) are unchanged — only the type changes, because every abort that reaches this frame is transient.
- The OpenAI and Gemini frames are untouched: they keep `server_error`, which their clients read.
- Verified with the new `TestBuildStreamErrorBytes_ClaudeUsesOverloadedError` and `TestBuildStreamErrorBytes_OpenAIKeepsServerError` plus the full `proxy` suite.
