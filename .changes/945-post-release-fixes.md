# 945 — Post-release fixes: fresh-boot schema index + DATA_DIR compat restore

Two defects from the 22 post-release commits (analysis:
docs/POST_RELEASE_DELTA_ANALYSIS.md):

- internal/db: idx_pc_last_used_at indexed the additive column lastUsedAt
  from the providerConnections table DDL, which runs before
  ensureAdditiveColumnsPG — a fresh database aborted boot with SQLSTATE
  42703 (upgraded databases already carried the column, so only fresh
  installs and the integration harness's throwaway schemas hit it). The
  index now sits beside idx_ak_lookup, created after the additive pass,
  per the rule that block already documents.
- internal/config: the port-3177 commit had also deleted
  preferredDataDir, silently reverting the rename's DATA_DIR contract
  (fragment 942): a pre-rename install whose state lives only in
  ~/.9router booted against an empty ~/.axonrouter-go. Restored verbatim.

Verified directly (no test suite): build + vet green; fresh scratch-DB
boot reaches login 200 (previously died in ProvideDatabase); a second
boot on the same database (upgrade path) stays clean; ResolveDataDir
checked under the three HOME shapes (legacy-only, both, neither).
