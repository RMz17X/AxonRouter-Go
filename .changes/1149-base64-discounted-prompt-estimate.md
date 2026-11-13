### Fixed
- Prompt-usage floor now covers image-carrying requests: the body-size prompt estimate discounts inline base64 media payloads instead of skipping those requests entirely, so a segment-counting upstream's prompt total no longer passes unfloored on histories that contain screenshots or images (the residual ~0% context-meter lane after v1.10.49).
