# 1003 — One log axis: request ID + serving account on every serving-path line

## Symptom

The middleware has minted a request ID (`req_<hex12>`) all along and the
access line prints it, but nothing else did: the `[gemini]`, `[usage]`,
`[proxy]` and `[fallback]` lines of the very same request carried no ID
at all, and no account name either — the `log.*Ctx` helpers that prepend
the ID existed with zero call sites in the chat handlers. Following one
request through the console meant eyeballing timestamps, which collide
under parallel bursts, and a stored request detail shared no identifier
with the log trail, so the two could never be joined exactly.

## Fix

- `internal/log` gains the account half of the axis, mirroring the
  request-ID pattern: `WithAccount(ctx, name)` / `AccountFromContext(ctx)`
  under a typed `AccountKey`. `outputCtx` now prepends `req_id` first and
  `account` second, ahead of the caller's own fields, on every `*Ctx`
  line. An empty account stays absent rather than rendering blank.
- The serving path stamps the account per attempt:
  `tryForwardWithConnection` wraps its context with the attempt's
  connection display name (`withConnAccount`: `connObjName`, falling back
  to the stored email — for antigravity the name is the account email)
  before anything derives from it, and the fallback loop stamps its
  post-attempt lines the same way, so each hop of a combo/fallback trail
  names the account that actually served it.
- 55 plain log calls on the serving trail converted to their `*Ctx`
  variants: the fallback loop and attempt body (client-cancel,
  upstream-failed, connection-locked, rotation lines), the Gemini-native
  forwarder (upstream response headers, stream ended, translate errors),
  the generic forwarder's stream/JSON handlers, token-saver lines, and
  the `[usage] logged` line itself.
- Proxy resolution joins the axis: `getClientForConnectionCtx` threads
  the request context through the pool pick / no-fit-pool / strict-mode
  lines; the context-free `getClientForConnection` remains for probes and
  admin callers and logs unadorned, as before.
- Request details record the axis: both chat-path detail payloads
  (success and failure) now carry `reqId` from the request context when
  one is present (absent, never zero-filled, on ID-less paths), and
  `models.RequestDetail` gains the matching `ReqID` field
  (`reqId,omitempty`). Rows stored earlier simply omit it.
- `GET /api/console-logs` accepts `?requestId=req_…` and returns only the
  lines containing that ID — one substring match catches both the access
  line's `id=` and the axis's `req_id=`. The SSE stream accepts the same
  parameter for its init dump and live lines. `?n=` caps the result to
  the last n entries and is applied **after** the filter, so it always
  means "the last n lines of the filtered trail".
- No dashboard change: the console page renders line text verbatim (and
  its search box now doubles as a request-ID filter).
