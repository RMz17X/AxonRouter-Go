## Antigravity safety: one identity per account, account-proxy egress, usage cache

The Antigravity suspend investigation (docs/ANTIGRAVITY_SUSPEND_ANALYSIS.md)
found the Go build presenting several different client identities for the
same account and sending background traffic from the server's own IP —
the two loudest signals distinguishing this router from the real clients
and from the JS build, which stayed unsuspended.

- Per-account identity is now consistent: the profile stored on the
  connection (CLI by default, IDE when chosen) drives the User-Agent on
  chat, media (image/search/STT), dashboard usage (including the weekly
  overlay and subscription-info calls) and the models fetch. The IDE
  profile no longer aliases to the CLI identity (`antigravity/2.23.0`,
  the real IDE app shape). The connection probe is unchanged — its body
  and headers are byte-identical to the JS build.
- Background OAuth refresh and dashboard usage fetches now egress
  through the account's proxy (connection binding first, then the
  provider's strategy pool), falling back to a direct connection only
  when no proxy is bound — the JS build's strictProxy=false semantics —
  instead of always using the server IP.
- GET /api/usage/{id} responses are cached for 55 seconds per
  connection (JS USAGE_RESPONSE_TTL_S parity); ?force=1 bypasses the
  lookup and refreshes the entry.
- Background refresh cadence is de-synchronized: the due list is
  shuffled every tick and the inter-account gap is jittered by +0-50%,
  so the fleet no longer refreshes in the same order on a fixed
  metronome (the code claimed jitter it did not have).
- Routing logs are mirrored to a rotating file
  (<data dir>/logs/routing.log, 20 MiB x 5; AXONROUTER_LOG_FILE
  overrides the path, "off" disables) so incidents can be analysed
  from disk afterwards.
- The quota tracker gains a name/email search (server-side `search`
  parameter on /api/providers/client) and cooldown reset actions —
  per-account and reset-all — wired to the bulk reset's cooldown
  action, which never touches disabled accounts. The provider detail
  page no longer auto-fetches usage for every connection on open;
  quotas load on demand via its Load quotas button.
