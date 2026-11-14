### fix(images): align /v1/images/edits with the OpenAI request contract

- The JSON lane rebuilt the upstream body from a fixed struct, so every field outside `image`, `prompt`, `model`, `size`, `response_format` and `n` was dropped before the request left the gateway. `input_fidelity`, `output_format`, `output_compression`, `background`, `quality` and `user` now reach the provider unchanged.
- The image reference is accepted in every shape the OpenAI SDKs send: a plain string, an array of strings, `{"image_url": {"url": ...}}` and `{"url": ...}`. Before the fix an array or an object failed to unmarshal and the request was answered 400 even though the payload was valid.
- `{"file_id": ...}` is refused with the upstream message and the request is not forwarded, because a file reference carries no bytes to send.
- `mask` is refused with the upstream message in both lanes. No provider lane reads it, so forwarding it advertised a capability that does not exist.
- Both lanes cap the image count at 16, the upstream count (`MAX_IMAGES`). Before the fix the two lanes carried different ceilings — 16 in the JSON lane, 8 in the multipart lane — and the global media guard added a third: it measures only media that carries bytes, so an image spelled as a URL was measured as zero items and one spelled as a data URI as one item. The same nine-image request was therefore forwarded as URLs and refused as data URIs. The count is now one number for the endpoint, enforced the same way in both lanes and for both encodings.
- The forwarded request is presented to the provider as `application/json` in both lanes.
- A body over the request ceiling is answered 413 naming the limit, not 400 reporting a malformed request.
- Multipart dispatch is case-insensitive: `Multipart/Form-Data` reaches the multipart lane instead of failing on a JSON parse error.
- An upload whose bytes are not a PNG, JPEG, WEBP or GIF is refused instead of being coerced to `image/png` and forwarded.
- `POST /images/edits` is now covered by the media body-size rule, so the per-item image ceiling is enforced on this path.
- Covered by nine regression tests in `internal/handlers/media/image_edits_parity_test.go`, each pinned to one finding of the independent review of #5548, plus `internal/handlers/media/image_edits_cap_encoding_test.go` for the count being independent of how the image is spelled.
- Remaining deviation: the endpoint refuses an over-cap request with 400, while the global media guard answers 413 for the same class of problem. The lane-level check runs first because it can name the endpoint's own limit, which is the more actionable of the two.
