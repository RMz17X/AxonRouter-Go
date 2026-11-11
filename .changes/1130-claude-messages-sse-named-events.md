### Fixed

- **Claude Messages SSE event framing**: Preserve upstream `event: <name>` headers (and infer from payload `"type"` when missing) during downstream Claude Messages streaming passthrough (`TranslateResp = true`). Previously, frames were emitted with only `data: ...` without the `event:` header, causing the Anthropic SDK in Claude Code / Claude CLI to discard all events and fall back to non-streaming JSON.
