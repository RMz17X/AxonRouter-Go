### Verification-required accounts: release verdict moves to a real generate; Verify link stores the account's real flow

- Fragment 1046's auto-recheck released flagged accounts on the light
  account probe (loadCodeAssist). Live evidence 2026-11-09 proved the
  surfaces disagree: for jayawijayaa116@gmail.com the probe answered
  valid while a real `streamGenerateContent` call with the account's
  own refreshed token returned 403 PERMISSION_DENIED /
  VALIDATION_REQUIRED, and the quota surface agreed with generate.
  The probe-based release therefore oscillated — release on the probe,
  re-park on the next quota read — and the operator's Refresh in the
  Quota Tracker could never actually enable an account.
- New `recheckValidationFlag` (dashboard): the release decision for a
  validation-flagged connection is a real generate through the
  ordinary chat lane with the connection pinned (the auto-ping
  machinery; probe-marked, so it never enters the usage ledger, and a
  probe pin is never redirected to another account). The account is
  temporarily reactivated so the pinned lane will serve it; a passing
  generate leaves it released, a failing one re-parks it with the
  freshest verdict. Both release sites use it: the quota-read
  auto-check (fragment 1046's hook) and the connection test itself
  (fragment 973's manual Recheck / the tracker's Check button now
  answer with the generate verdict for flagged Antigravity accounts
  instead of the probe's).
- The Verify link now stores the account-specific verification URL
  the upstream error carries (`error.details[].metadata.validation_url`,
  extracted by a recursive walk of the error payload) at every flag
  writer that sees a fresh error: the generate recheck, and the quota
  surface (`fetchAntigravityDashboardUsage` surfaces it on the usage
  result). The URL's token rotates with each fresh error, so every
  recheck also refreshes the stored link — the operator always opens
  the current flow Google actually gated, not the generic
  `https://antigravity.google` landing page that fragment 1046's code
  stored. The chat-path writers already extracted this URL
  (`extractGoogleValidationURL`); the generic page remains only as a
  fallback when an error carries no URL.
- Non-validation ping failures (transport, timeout) re-park with the
  previously stored verdict instead of overwriting it with noise:
  flagged is the conservative state, and the next read retries the
  check.
