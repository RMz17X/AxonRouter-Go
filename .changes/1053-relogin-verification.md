### Verification is a re-login: flagged Antigravity accounts reactivate through the OAuth sign-in, not the bare error URL

- Mechanism, established live (2026-11-09): VALIDATION_REQUIRED is
  Google's first-use gate — loadCodeAssist lists the free tier under
  `ineligibleTiers` with reasonCode VALIDATION_REQUIRED for accounts
  that have never used Antigravity. The gate lifts account-wide when
  the account completes a real sign-in with the Antigravity client
  (what agy's login does), and ONLY then: the `validation_url` minted
  into API errors — and even the one in loadCodeAssist's
  ineligibleTiers — opened bare in a browser errors (owner saw a 500)
  or "succeeds" without registering anything; all four flagged
  staging accounts stayed generate-gated after repeated browser
  completions, with fresh post-verification tokens, on every surface
  combination probed (with/without project, daily/prod hosts).
- Backend (`oauth/antigravity.go`): the exchange already keyed the
  connection by the sign-in email, so a re-login lands on the same
  row and rebuilds its blob (validation fields drop out). It now
  also detects that the replaced blob was validation-flagged and
  reactivates the connection (`isActive = 1`) in the same UPDATE,
  reporting `reactivated: true` in the response. Manual and ToS
  disables are deliberately NOT reactivated by a re-login. If Google
  still refuses the account after the sign-in, the serving-path
  classifiers re-flag it on the next refusal, so the state stays
  honest.
- Dashboard: the flagged row's primary action on the provider page
  is now **re-login** (runs the provider's OAuth connect modal —
  Google's verification rides inside that sign-in, exactly like
  agy), next to the existing generate-oracle Recheck. The error
  detail panel's "Buka Link Verifikasi Manual" became "Login Ulang
  untuk Verifikasi" doing the same. Quota Tracker no longer sends
  operators to the dead bare link; its two callouts now say the
  account needs a re-login on its provider page (the tracker keeps
  its generate-based Check button). Deliberate divergence from the
  JS reference, which still links the raw validation_url: that path
  is proven not to register, so Go does not offer it as the action.
