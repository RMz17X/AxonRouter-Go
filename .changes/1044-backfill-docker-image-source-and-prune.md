### Backfill: docs suite, compose image sourcing, repo prune (ErNor, shipped in this release)

- A standard core documentation suite now lives in `docs/` and is
  linked from the README (commits 976eace5a9, d189730d79).
- The single and cluster compose files now pull the published GHCR
  image instead of building from source; local source builds move to
  the dedicated `docker-compose.build.yml` (commit 2e154b0e).
- Pruned orphaned root screenshots, an empty db artifact, and stale
  discovery notes from the repo (commit ebc5334a).
