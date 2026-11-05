# AxonRouter-Go — post-release delta analysis (2026-11-05, Jajang)

Scope: `1b2f86a5` (Jajang's verified local release) → `fb75966f` (hijilaptop/GitHub
main, 22 commits, 98 files, +2206/−618). Analysis by diff reading + one live
fresh-boot reproduction on this VM. The release tree itself is not implicated
in any finding below.

## What the 22 commits are (themes)

1. **Performance wave (the bulk)** — dashboard stats via direct SQL aggregation
   instead of loading ~170k connection rows; VaultCounts fast path; hot-path
   reads served from an in-memory provider-connections snapshot (15 s TTL,
   same-process writers invalidate immediately); providerConnsMu → RWMutex;
   PG pool defaults 10/5 → 50/25; three new providerConnections sort indexes;
   cluster-compose PostgreSQL tuning (shared_buffers=1GB, work_mem=64MB,
   effective_cache_size=8GB, …).
2. **Proxy-pool intelligence ("smartfit")** — per-provider proxy-group locking
   and auto-inheritance for accounts; per-connection proxy dropdown with search
   (UI); realtime requests gain a proxy column + egress-pool tracking; failure
   taxonomy wired into the chat fallback loop: location-policy rejection
   (FAILED_PRECONDITION) locks the pool 24 h (fitness tracker + persisted
   poolstate mark), transport errors penalise fitness and exclude the pool for
   the request, successes record latency. Cooldown ladder steepened
   10s/30s/60s → 30s/2m/10m/30m/1h; from the 2nd consecutive failure the pool is
   also marked unfit in poolstate. PickBest now consults poolstate and REJECTS
   a single candidate that is unfit/cooling (previously returned it).
3. **Branding/rename completion** — official AxonRouter logo/favicon/PWA icons
   replace the legacy 9router assets; media/CLI-tools sweep renames the wire
   field `has9Router` → `hasAxonRouter`; Grok model catalog update
   (4.7/4.6/4/3/code-fast) + live-catalog timeout tuning; minimax-code-global
   choices translation fix; Tencent/CodeBuddy displayMsg/msg error extraction.
4. **Product/ops defaults** — default gateway port 20130 → **3177** everywhere
   (config, Dockerfile ENV/EXPOSE/healthcheck, both composes, nginx upstream);
   default DATA_DIR resolution simplified (see Finding 2); GitHub Actions
   workflow publishes the Docker image to GHCR on `v*` tags (PAT secret);
   AGENTS.md gains a "zero-tolerance bottleneck auto-fix" rule (ca2eb1a5).
5. **UI polish** — K/M/B number compaction across tables/sidebar; topology
   blocking overlay text removed; ProviderDetailView heavily reworked
   (478 lines changed; includes a ReferenceError fix).

## Findings

### F1 — BLOCKER: fresh install cannot boot (proven live)
Commit af166835 added three indexes to the providerConnections table DDL in
`internal/db/schema_pg_defs.go`; one of them,
`idx_pc_last_used_at ON providerConnections(lastUsedAt …)`, indexes
`lastUsedAt`, which is an ADDITIVE column (defs line 383) created by
`ensureAdditiveColumnsPG` — a pass that runs AFTER the table DDL inside
`EnsureCoreSchema`. On an empty database the index creation fails:
`column "lastusedat" does not exist (SQLSTATE 42703)`, and ProvideDatabase
aborts the boot. Reproduced live on this VM 2026-11-05 23:45 WIB (fresh
scratch DB, release-line env). Upgraded databases survive because a previous
boot already added the column — which is why the hijilaptop deployment never
noticed. The codebase already documents this exact rule: `idx_ak_lookup` on
apiKeys(lookupHash) is created after the additive pass, with a comment saying
the table DDL cannot index an additive column on a fresh database. Fix: move
`idx_pc_last_used_at` to the same post-additive position (one-line move; the
other two new indexes reference base columns and are fine). Side effect: the
integration harness provisions throwaway schemas via the same
`db.EnsureCoreSchema` (harness_test.go:103), so the suite currently cannot
boot either — the new line has no runnable release gate until this is fixed.

### F2 — Owner-decision regression hidden in the port commit
Commit dda8b363 (port 3177) also deleted `preferredDataDir` — the rename's
deliberate DATA_DIR compatibility: an install whose state lives only in
`~/.9router` keeps resolving there. Without it, pre-rename installs boot
against an empty `~/.axonrouter-go` (CLI tokens, machine-id, uploads, tunnel
state orphaned on disk). This contradicts the recorded rename decision
(.changes/942) and was not mentioned in the commit message.

### F3 — Watch items (deliberate trade-offs, not defects)
- PG pool defaults 50/25 per node + cluster PG tuning sized for a large host;
  against a small PG (max_connections=100) or the 1-vCPU target box this is
  oversized — env overrides exist (PG_POOL_MAX_OPEN/IDLE, PG_MAX_CONNECTIONS).
- Cooldown ladder 30s→1h and single-pool rejection make the gateway stricter:
  a deployment whose only pool is cooling fails requests instead of trying
  the pool. Availability trade-off of the smartfit design.
- Connection snapshot cache is per-process (15 s TTL): in cluster mode,
  another node's connection writes are visible after up to 15 s — same class
  as the recorded semantic-cache note.
- VaultCounts semantics shifted: rows with no ciphertext now count as
  plaintext wholesale (previously credential-less rows fell in an ambiguous
  branch) — dashboard numbers may jump without any security change.
- `has9Router` → `hasAxonRouter` is a deliberate wire-field break (SPA
  updated in the same sweep; external consumers would break).

## Verdict
Direction is coherent (performance + proxy intelligence + brand completion)
and the release remains a clean ancestor. But the new line currently ships
with a fresh-install boot blocker (F1) and one silent compat revert (F2);
both are small, precise fixes. Until F1 lands, no fresh deployment and no
suite run can succeed on fb75966f.

## Resolution (2026-11-05, Jajang — at the owner's order)

- **F1 FIXED**: `idx_pc_last_used_at` moved out of the providerConnections
  table DDL into the post-additive-columns position in `EnsureCoreSchema`,
  next to `idx_ak_lookup`, following the pattern the codebase already
  documented. Verified by a fresh-database boot (see the fix commit).
- **F2 FIXED**: `preferredDataDir` and the legacy `~/.9router` DATA_DIR
  fallback restored in `internal/config/config.go`, verbatim from the
  release tree.
- **F3 items intentionally left unchanged**: they are the hijilaptop line's
  deliberate design choices (pool sizing/tuning, smartfit cooldown
  strictness, VaultCounts fast-path semantics, the `hasAxonRouter` wire
  rename), not defects.
