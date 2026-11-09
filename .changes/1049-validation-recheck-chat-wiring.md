### Verification-required recheck: wire the chat handler into the production dashboard route table

- Fragment 1048's generate-backed recheck shipped in v1.9.63 but
  could never run in production: `recheckValidationFlag` refuses to
  guess when the dashboard handler has no chat handler, and the
  production dashboard table never had one. `SetupRoutes` wired
  `dashH.Chat` for its own table, but the server router mounts the
  dashboard API through `SetupDashboardRoutes`, which wired
  SemanticCache, Limiter and Resilience from the chat handler and
  left Chat nil — so every quota-read recheck exited at the guard,
  flagged accounts stayed parked with their pre-fix verdicts, and
  the connection test's generate verdict silently fell back to the
  stored message. Proven on staging v1.9.63: usage reads on flagged
  accounts returned in ~2 ms with the stored lastError timestamps
  untouched (no recheck writes at all), while the code was present
  and the version read 1.9.63.
- `SetupDashboardRoutes` now wires `dashH.Chat = chatH` alongside the
  other shared instances. This also gives the production playground
  routes the chat handler their dispatch expects.
- The recheck's re-park also keeps the freshest verification URL:
  when the ping's error body yields no URL, it re-reads the stored
  one before falling back, because the ping's own chat-lane
  classifier may have just stored a newer token from the same
  refusal than the one the recheck started with.
