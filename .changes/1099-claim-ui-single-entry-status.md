# MiniMax claim UI: one entry point, claimed state honoured (1099)

- Quota page: the duplicate "Daily Check-in Reward / Claim Points" banner is
  removed; the header Claim button is the single claim entry point.
- The header slot is now driven by the claim-status endpoint, read once per
  visible MiniMax row during the quota pass: already claimed today renders a
  non-interactive "Day N" chip instead of a button; ready (todayStatus 2)
  renders the Claim button; a failed status read fails open to the button.
  A successful or already-claimed claim response marks the row claimed
  locally, so the button disappears without a refetch; Claim-all resets the
  status map and the refresh pass re-reads it.
- Backend: GET minimax-claim status responses are cached for 5 minutes per
  connection (claim_status_cache.go), so a page pass no longer fans out into
  one live upstream read per account on every refresh. Single and bulk
  claims invalidate the cache immediately.
