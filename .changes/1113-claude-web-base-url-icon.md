---
type: fixed
---

**claude-web** now talks to the host that actually exists. The spec named `https://api.claude.ai` as the upstream base, but that name does not resolve — proven live from the staging host (no DNS answer, connection failure) while `claude.ai` resolves and answers the same `/api/...` paths (403 without a session, as expected). The executor base URL, the connection-test probe (`/api/auth/current_account`), the provider row, and the spec document itself now use `https://claude.ai`. The release also ships the provider's artwork, `web/public/providers/claude-web.png`, so the dashboard shows the Claude logo instead of the initials fallback badge.
