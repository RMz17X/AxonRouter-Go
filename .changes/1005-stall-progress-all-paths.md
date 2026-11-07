# 1005 — Stall-timeout progress now reported on every serving path, not just antigravity/gemini

## Symptom

Fragment 1004 turned the combo response timeout into a stall timer, but
progress was only reported from the two chat-handler paths that had manual
ping call sites (the generic forwarder and the Gemini-native handler).
Every other route a combo candidate can take stayed invisible to the
gate: the Responses streaming bridge reads the upstream inside the
executor package, the executor fleet (Responses-format OpenCode lanes,
Azure, Commandcode, the browser-web providers, Trae, Windsurf, Freebuff,
the stealth transport) reads bodies the chat package never touches, and
the single-shot JSON body reads — including the antigravity unwrap — sat
inside one `io.ReadAll` with no sign of life after the arrival ping. A
healthy-but-slow candidate on any of those paths could still be cut at
the silence window while its body was in fact trickling, and a stalled
one was indistinguishable from it.

## Root cause

The progress helper (`noteUpstreamProgress`) and the gate's context key
were unexported inside `internal/handlers/chat`, so no other package
could report progress; and progress was wired per call site instead of
at the point each path receives its response body — so any path nobody
had hand-pinged simply never reported.

## Fix

- The registry moved to where the other request-context keys already
  live: `internal/log` now owns `ProgressGate` (the one-method interface),
  `RegisterProgressGate`, `NoteUpstreamProgress`, and `ProgressReader`.
  The combo gate registers through it in `combo_timeout.go`; chat's
  `noteUpstreamProgress` delegates to the same registry. There is exactly
  one registry mechanism — the chat-local key and interface are gone.
- `log.ProgressReader(ctx, body)` returns a body wrapper that notes
  progress on every `Read` returning bytes. When the context carries no
  gate it returns the body unwrapped, so direct requests and a combo's
  last candidate are byte-for-byte unaffected.
- The wrapper is applied at the response-receipt point of every serving
  path a combo candidate can take: the shared sender `proxy.DoRequest`
  (which covers `ForwardOpenAI`, the Codex/Kiro/Iflow/Kimchi/Grok-CLI
  helpers, Bedrock, ZCode, Qoder, Zed, Minimax-Code and every executor
  built on them), the Gemini-native receipt in the chat handler (covers
  `ForwardGemini`'s internal signature retries, the non-stream read and
  the antigravity unwrap read), and each executor that dials on its own:
  Azure and Commandcode (`providers.go`), the OpenCode stealth transport,
  Trae's session POST and events stream, Windsurf, Freebuff's
  `DoFreebuffHTTP`, and the DeepSeek/Gemini/Grok web completion receipts.
  Downstream consumers — the Responses bridge, the Claude-messages
  translation stream, the codebuddy SSE lane — read bodies received at
  those points, so their reads now report too. The existing handler pings
  in `forward.go` and `gemini_handler.go` are unchanged.
- Gate semantics are untouched: silence-only cut, permanent disarm on
  commit, no resurrection after expiry, benching/grace and the
  last-candidate exemption exactly as before.

## Verification

- `go build ./...` green; `go vet` clean on `internal/log`,
  `internal/handlers/chat`, `internal/proxy`, `internal/proxy/executor`.
- Temporary probe `TestProbeProgressReader` in `internal/log` (created,
  run alone, then deleted): a gate-registered context over a chunked
  reader produced exactly one progress note per chunk with bytes intact;
  a gateless context returned the original body unwrapped with identical
  bytes; a pinged gate outlived its 80 ms window across five chunks
  30 ms apart while a silent gate expired.
