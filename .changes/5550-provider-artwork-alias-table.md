### fix(web): the provider-artwork check followed a stale hardcoded list

- The test that asserts every catalog provider ships an artwork file carried its own list of ids whose artwork is filed under another provider (`zai-search`, `zcode`). That list went stale when `deepai-free` was aliased to `deepai`, so the check failed on a provider the dashboard renders correctly — the alias resolves before the file is requested, and no request 404s.
- The alias table is now the single source of truth for that check, so a new alias no longer has to be mirrored in a second place.
- The check also asserts that every alias still points at artwork that exists. A dangling alias now fails here instead of 404ing in the browser on every surface that draws the provider.
