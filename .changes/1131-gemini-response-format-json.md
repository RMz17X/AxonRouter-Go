### Added

- **Gemini response_format normalization**: Translate OpenAI `response_format: {"type": "json_object"}` and `json_schema` into Gemini native `generationConfig.responseMimeType = "application/json"`, `responseSchema`, and system guidance. This guarantees that requests specifying JSON formats strictly return pure JSON without markdown wrappers or conversational filler across Gemini and Antigravity models.
