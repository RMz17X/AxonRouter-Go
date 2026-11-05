### refactor(db): schema bootstrap runs in three phases — tables, additive columns, then all indexes

**Symptom.** The PostgreSQL bootstrap created each table's indexes as
part of creating the table, while the additive columns
(`pgGoOnlyColumns`) were only applied near the end. Any index declared
on an additive column therefore detonated on fresh databases — this is
the exact mechanism behind the v1.9.16 fresh-install boot blocker
(fragment 945), and the two indexes that need additive columns survived
only as hand-placed one-off `Exec` calls after the additive pass,
detached from their tables' declarations.

**Root cause.** Creation order was coupled to declaration order:
`ensureTablePG` fused table creation, column backfill and index
creation into one per-table step that ran before the additive pass, so
the ordering rule ("never index a column that does not exist yet")
lived in comments and in two exceptional code blocks instead of in the
structure.

**Fix.** `EnsureCoreSchema` now runs three explicit phases: (1) every
table group (core, KeiRouter, usage ledger, thought signatures) plus
the partitioned parents is created and backfilled with its declared
columns; (2) the additive columns are applied; (3) every declared
index is created. `ensureTablePG` is split into
`ensureTableColumnsPG` / `ensureTableIndexesPG`, the partitioned
parents' defs are factored into `partitionedUsageParents()` with their
indexes executed by `ensurePartitionedIndexesPG`, and the two stray
indexes return home: `idx_ak_lookup` into the apiKeys def and
`idx_pc_last_used_at` into the providerConnections def. A new index on
any column — declared or additive — is now safe by construction,
wherever it is declared.

**Verification.** Behaviour-preserving, proven by catalog read-back,
not by assertion: the full schema (tables, columns with types and
defaults, indexes with definitions, PK/UNIQUE constraints — 425
objects) produced by the pre-refactor code on a fresh database was
captured as a golden snapshot; the refactored code's fresh-database
schema is byte-identical to it, the refactored code booted on the
pre-refactor database leaves the schema identical (upgrade path), and
a second boot is clean. Build and vet green; login 200 in all three
boots; zero error lines in the boot logs.
