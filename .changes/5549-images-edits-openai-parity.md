### fix(images): align /v1/images/edits with the OpenAI request contract

- The JSON lane rebuilt the upstream body from a fixed struct, so every field outside `image`, `prompt`, `model`, `size`, `response_format` and `n` was dropped before the request left the gateway. `input_fidelity`, `output_format`, `output_compression`, `background`, `quality` and `user` now reach the provider unchanged.
- The image reference is accepted in every shape the OpenAI SDKs send: a plain string, an array of strings, `{"image_url": {"url": ...}}` and `{"url": ...}`. Before the fix an array or an object failed to unmarshal and the request was answered 400 even though the payload was valid.
- `{"file_id": ...}` is refused with the upstream message and the request is not forwarded, because a file reference carries no bytes to send.
- `mask` is refused with the upstream message in both lanes. No provider lane reads it, so forwarding it advertised a capability that does not exist.
- The JSON lane applies the same image cap as the multipart lane and refuses a request above it instead of forwarding an unbounded list. The cap is 8 media items per request, the same ceiling the other media endpoints use, so both lanes of this endpoint agree. The upstream 9router accepts 16; the lower number is deliberate and matches the rest of this gateway, and the refusal names both counts so the operator can see which limit applied.
- The forwarded request is presented to the provider as `application/json` in both lanes.
- A body over the request ceiling is answered 413 naming the limit, not 400 reporting a malformed request.
- Multipart dispatch is case-insensitive: `Multipart/Form-Data` reaches the multipart lane instead of failing on a JSON parse error.
- An upload whose bytes are not a PNG, JPEG, WEBP or GIF is refused instead of being coerced to `image/png` and forwarded.
- `POST /images/edits` is now covered by the media body-size rule, so the per-item image ceiling is enforced on this path.
- Covered by nine regression tests in `internal/handlers/media/image_edits_parity_test.go`, each pinned to one finding of the independent review of #5548.
