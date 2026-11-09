## Antigravity verification: verification URL only — the re-login button is removed (v1.9.70)

- **Owner's call (2026-11-09):** the re-login button is unnecessary and must go. Verification is the account's verification page, as in the real client — nothing else.
- **Change:** every login-ulang surface from v1.9.68/v1.9.69 is removed. The provider page flagged row again offers the account's own stored verification URL directly ("verifikasi", new tab) beside the generate-oracle Recheck; the error panel shows the single "Buka Verifikasi" action; the Quota Tracker's two guidance paragraphs become direct "Verifikasi Akun" links again. No sign-in step, no sequencing copy.
- **Kept:** the v1.9.68 exchange-side reactivation (an OAuth exchange that lands on a validation-flagged row reactivates it) — invisible, fires only if the account is ever re-added through the normal connect flow; the UI simply never asks for it. The generate-oracle recheck (v1.9.63/64) is unchanged.
- **Verification:** build + svelte-check gates at release; staging read-back of /api/version after deploy.
