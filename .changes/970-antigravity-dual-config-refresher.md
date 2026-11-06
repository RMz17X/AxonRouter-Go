## Antigravity OAuth: dual-config refresher — the client swap is now a config flip

Fragment 969 rebased every Antigravity surface onto the real `agy` CLI
identity and left exactly one residual: the OAuth client. `agy` signs in
with its own client (the 884354919052 pair), while this router's
`KnownOAuthConfigs["antigravity"]` still holds the pre-rebase
1071006060591 pair — the pair `agy` itself carries as the secondary
config in its RunLogin. This change builds the full dual-config
architecture that mirrors that RunLogin, so the swap itself is no
longer a code change: it is a one-step flip of the primary slot's
values, whenever the 8843 pair lands.

- `internal/providers/oauth.go` gains `AntigravityLegacyOAuthConfig`,
  a named home for the pre-swap client. It is fenced to one role —
  refresh rescue — and is never used for sign-in or code exchange: the
  sign-in handler continues to seed from `KnownOAuthConfigs` only.
  Today it intentionally holds the same pair as the primary slot: the
  day the primary flips, this slot already holds the client every
  existing account's refresh token was minted under.
- New `internal/proxy/oauth/antigravity.go` registers a custom
  Antigravity refresher (so `RegisterAll` no longer substitutes the
  plain standard one). Selection rules:
  - Refresh is attempted with the PRIMARY config first, exactly as
    before.
  - Exactly ONE rescue attempt under the legacy config runs when —
    and only when — the primary attempt is rejected as a client
    mismatch: HTTP 401, or HTTP 400 whose error body names
    `invalid_client` or `invalid_grant` (Google's answer when a
    refresh token is redeemed under a different client than the one
    that minted it). The classifier is deliberately narrower than
    `providers.IsRefreshGrantDead`: `invalid_request` and friends are
    terminal for the grant itself, not evidence about client
    ownership, and never trigger a rescue.
  - If the rescue also fails, the PRIMARY attempt's error is
    returned, so grant-dead parking and every other caller behavior
    see exactly the error they would have seen with no rescue
    configured.
  - Non-mismatch failures (transport, 5xx, quota) never trigger the
    rescue: they say nothing about which client owns the grant.
  - Skip-when-same-ID: while both slots name the same client (the
    state until the flip), the rescue is skipped rather than resending
    the identical grant under the identical client — so today's wire
    behavior is unchanged.
- Coverage is the `oauth.Refresh` registry, which every Antigravity
  refresh path already funnels through: the background refresher
  (`internal/proxy/oauth/background.go`), the dashboard usage
  credentials refresh (`internal/handlers/dashboard/usage_credentials.go`),
  the connection probe, and the chat force-refresh's first attempt.
  The chat handler's generic fallback after a registry failure (a
  primary-config-only `providers.RefreshToken`, shared by all OAuth
  providers) is unchanged and never touches the legacy config.
- The remaining flip, when the 8843 secret is available: replace the
  primary slot's values with the 884354919052 pair — via the
  `ANTIGRAVITY_OAUTH_CLIENT_ID` / `ANTIGRAVITY_OAUTH_CLIENT_SECRET`
  env overrides or the literals in `KnownOAuthConfigs`. Nothing else
  moves: sign-in and code exchange pick the new primary up from the
  same seed, and the rescue slot is already correct for pre-flip
  accounts. If a refresh ever fails under BOTH clients (a revoked
  grant, not a mismatch), the account is parked as grant-dead and
  needs a fresh sign-in — the same consequence as before this change.
