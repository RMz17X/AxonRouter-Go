### Verification-required accounts: automatic recheck on quota reads + a Check button in the Quota Tracker

- Root cause of "I verified the account but it stays disabled": the
  only release path was the manual Recheck on the provider page. The
  quota usage endpoint short-circuits every inactive connection to its
  stored error without any live read, so the Quota Tracker — where the
  operator watches these accounts — could never observe that Google
  accepts the account again, and no automatic check existed.
- `HandleGetConnectionUsage` now special-cases a validation-flagged
  connection (`data.status = validation_required`, the non-terminal
  verdict carved out in fragment 973): the read runs the same probe
  the connection test uses, and `persistProbeResult`'s existing
  verified-transition clears the flag and reactivates the account;
  the read then falls through and serves live quotas. Any other
  verdict keeps returning the stored message. Terminal disables
  (ToS, dead auth) are still never dialed.
- The Quota Tracker row actions gain a Check verification button for
  flagged accounts (the tracker's counterpart of the provider page's
  Recheck): it re-probes via the connection test endpoint and force-
  refreshes the row, so the verdict is visible in place — released
  rows flip straight to their quota table.
