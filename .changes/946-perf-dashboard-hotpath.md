### perf: dashboard reads and the chat hot path stop scanning whole tables

- Provider summary stats are computed by direct SQL aggregation instead of
  loading every connection row (the dashboard was dumping ~170k connections
  to render a summary). VaultCounts gained a fast path: rows with no
  ciphertext are counted with COUNT(*), and only ciphertext-bearing rows
  still get deep JSON inspection.
- The chat hot path serves connection data and model-lock checks from an
  in-memory snapshot of provider connections (15s TTL; writers through the
  same Repo invalidate immediately), and providerConnsMu is an RWMutex with
  the clone hoisted out of the critical section.
- PostgreSQL pool defaults raised 10/5 → 50/25 for high SSE concurrency,
  and the cluster compose stack ships explicit PG tuning (shared_buffers,
  work_mem, effective_cache_size, WAL/checkpoint settings).
- Three sort indexes on providerConnections (priority/updatedAt ordering,
  provider+active+sort, lastUsedAt) back the new query shapes.
