### feat(proxy): smartfit proxy-pool intelligence

- Providers can lock a proxy group, and accounts inherit the provider's
  pool automatically; the dashboard gained a per-connection proxy dropdown
  with search and auto-lock indicators, lightweight proxy-group chips on
  no-auth providers, and a proxy column on realtime requests wired to
  egress-pool tracking.
- The chat fallback loop now classifies proxy failures: a location-policy
  rejection (FAILED_PRECONDITION / user location not supported) locks the
  pool for 24h through the fitness tracker plus a persisted poolstate
  unfit mark; transport errors penalise pool fitness and exclude the pool
  for the rest of the request; successes record latency into fitness.
- The fitness cooldown ladder steepened from 10s/30s/60s to
  30s/2m/10m/30m/1h, a second consecutive failure also marks the pool
  unfit, and pool selection consults poolstate: a single candidate that
  is unfit or cooling down is now rejected instead of used.
