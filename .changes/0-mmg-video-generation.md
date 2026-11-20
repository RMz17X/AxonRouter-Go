### MiniMax image and video generation

- Add H3 video generation to the existing `minimax-code-global` lane: `mmg/MiniMax-H3`, `mmg/MiniMax-H3-Highspeed`, `mmg/MiniMax-H3-Lite`, and `mmg/MiniMax-H3-Max` now route to MiniMax's `/v2/video_generation`, with URL-based job polling and credential-free CDN download.
- Translate OpenAI-style create bodies into the H3 payload, validate model-specific duration/resolution limits and public media references, forward `Retry-After` on quota and in-progress responses, and rotate accounts only on authentication failure.
- Keep MiniMax Code, MiniMax China, and UC Web strictly separate: no provider alias, substring routing, or chat-endpoint change was added.
- Operator decision: video rides the existing `mmg` credential instead of a new provider, deliberately diverging from the H3 spec's separate-provider recommendation.
- Add MiniMax H3-Image generation on `mmg`: `MiniMax-H3-Image` and `MiniMax-H3-Image-Highspeed` route to the global OpenAI-shaped image endpoint, with prompt/size/aspect validation and a 10-minute response-header timeout. The endpoint is non-idempotent, so the gateway does not replay it.
- Restore upstream image parity for API-key `minimax/minimax-image-01` via `https://api.minimaxi.com/v1/images/generations`.
- Register image models in the Go and web catalogs with explicit names, parameter lists, and kinds; dashboard `serviceKinds` for `mmg` now includes `image` alongside `llm` and `video`.
