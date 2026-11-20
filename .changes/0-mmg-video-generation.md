### MiniMax Code global video generation (mmg/)

- Add H3 video generation to the existing `minimax-code-global` lane: `mmg/MiniMax-H3`, `mmg/MiniMax-H3-Highspeed`, `mmg/MiniMax-H3-Lite`, and `mmg/MiniMax-H3-Max` now route to MiniMax's `/v2/video_generation`, with URL-based job polling and credential-free CDN download.
- Translate OpenAI-style create bodies into the H3 payload, validate model-specific duration/resolution limits and public media references, forward `Retry-After` on quota and in-progress responses, and rotate accounts only on authentication failure.
- Keep MiniMax Code, MiniMax China, and UC Web strictly separate: no provider alias, substring routing, or chat-endpoint change was added.
- Operator decision: video rides the existing `mmg` credential instead of a new provider, deliberately diverging from the H3 spec's separate-provider recommendation.
