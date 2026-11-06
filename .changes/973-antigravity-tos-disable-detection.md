## Antigravity: generation-path ToS detection now disables on every surface

The JS pool post-mortem (2026-11-06) proved the masking mechanism: a
ToS-disabled account keeps answering on the quota/loadCodeAssist path,
so any check that reads quota state reports it healthy while generation
is dead. Detection therefore has to come from generation traffic itself.
This change surveys what ErNor's 5ee02c10 already wired, and closes the
surfaces it missed.

### What already existed (survey, kept as-is)

- Status model: `Repo.DisableConnectionWithReason` sets isActive=0,
  data.status/testStatus="disabled", data.lastError {status, message,
  timestamp}, and appends the appeal form URL to ToS-shaped reasons.
- Chat generic fallback (`fallback.go`, mirrored in `combo.go`): a 403
  whose extracted error text names the ToS phrases disables with the
  verbatim text.
- Background OAuth refresh skips disabled connections at selection and
  again at refresh time; `GetActiveOAuthConnections` excludes them; the
  chat force-refresh refuses inactive rows. Dashboard single-usage fetch
  early-returns the stored error for inactive connections without
  touching Google. Selection queries filter isActive=1 and the disable
  write invalidates the connection cache, so rotation drops the account
  from the moment of disable.
- Quota Tracker and Provider Detail already render the stored error
  with the appeal link (https://forms.gle/hGzM9MEUv2azZsrb9) for
  ToS-shaped messages; the tracker never quota-fetches disabled rows,
  and its per-row reset button renders only for active connections.
- The provider-wide bulk "reset all cooldowns" already skipped
  inactive connections.

### Gaps closed

- **Media generation never disabled.** Image, STT and search handlers
  classified a ToS 403 through the generic rules and parked the account
  on the 24h ToS cooldown, then kept serving it. All three now check
  the shared signature first and disable permanently.
- **One signature, one home.** `providers.LooksLikeToSDisabled`
  (internal/providers/tos_disabled.go): a 403 whose body carries
  TOS_VIOLATION, "disabled in this account", or "violation of (the)
  Terms of Service". Deliberately narrower than the phrase lists it
  replaces at these sites: PERMISSION_DENIED alone is not the signature
  (VALIDATION_REQUIRED carries it), non-403 statuses never are (a 429
  quoting the sentence is a throttle), and quota/loadCodeAssist
  responses are never used as detection input. The native Gemini chat
  path and both fallback chains match it against the raw body in
  addition to their existing extracted-text phrases, so a verdict whose
  message is vague but whose status/reason tokens carry the signature
  still lands.
- **Native chat stored a paraphrase.** gemini_handler.go disabled with
  the canned string "Account disabled for ToS violation"; it now
  stores the verbatim upstream verdict ("Account disabled: " + message,
  status and reason tokens — via `providers.GoogleErrorText` in the
  media lanes, the chat extractor in chat), the same shape the fallback
  path stores, so the dashboard and appeal flow quote Google, not us.
- **The connection probe dialed Google for dead accounts.**
  HandleTestConnection ran the full probe (token refresh +
  loadCodeAssist) on a disabled connection, and persistProbeResult
  could overwrite the disabled verdict with a transient one. The probe
  now returns the stored disabled state without dialing, at both the
  single-test entry and inside testSingleConnection (batch callers),
  and persistProbeResult refuses to write over a stored disabled state.
  Only the stored disabled state is guarded — an operator-toggled-off
  or OAuth-parked (inactive, not disabled) account can still be probed
  manually.
- **Cooldown reset could revive a disabled account.** Bulk
  reset-status with action "cooldown" and an explicit id list (the
  Quota Tracker's per-row reset shape) reactivated the account and
  wiped its verdict, despite the UI intending "disabled accounts are
  never touched". In cooldown mode the handler now skips any inactive
  or stored-disabled connection however it was targeted. The explicit
  full reset (single reset-status, bulk without the cooldown action)
  remains the operator's deliberate reactivation path, as in JS where a
  manual reset-status clears the ToS marker after a successful appeal.

### Residual limits

- Detection is traffic-driven only: an account that is disabled while
  idle is caught on its next generation call, not before. No active
  probing was added (a probe is itself a generation-shaped call against
  an account Google is watching, and the quota path cannot substitute).
- In-band SSE errors after a 200 header are not classified; Google's
  disable verdict is a header-level 403 in every observed case.
- The usage/subscription fetch path can still disable an account when
  its response carries the verdict (pre-existing ErNor behavior); it is
  not relied on for detection and cannot mask a disable, only add one.
