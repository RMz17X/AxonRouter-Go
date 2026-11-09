### Persona owned_by branding, persona snapshot cache, audit cleanups

- `/v1/models` branding (owner order): combo entries now publish
  `owned_by: "AxonRouter"` and Persona virtual models publish
  `owned_by: "Axonrouter_Unrestricted"` (previously `"combo"` and
  `"persona"`). No consumer keyed off the old values.
- Persona definitions are now served from an immutable in-memory
  snapshot on the Repo (mirroring the jailbreak ruleset cache) instead
  of a kv query per lookup: the serving path asks "is this name a
  persona?" on every request and every failover attempt, so the answer
  must not cost a database round trip. Persona writes invalidate the
  snapshot; a failed rebuild degrades to "not a persona" rather than
  failing the request.
- The Persona modal no longer refetches the combo list: the Persona
  page's combos are passed in and shared with the target picker (the
  modal still lazy-loads connections, and combos only if the page's
  fetch came back empty).
- Retired-binding cleanups from the UX/efficiency audit: the global
  excludes copy and the jailbreak comments no longer describe the
  deleted per-model exact bindings as live behaviour, and the unused
  frontend wrapper for the retired binding-catalog endpoint was
  removed (the endpoint itself stays).
