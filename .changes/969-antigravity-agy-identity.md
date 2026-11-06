## Antigravity identity: rebased from the IDE profile to the real `agy` CLI

Fragment 968 made every Antigravity surface present the identity stored
on the connection — CLI by default, IDE when chosen. That was still two
clients too many: the suspend analysis (docs/ANTIGRAVITY_SUSPEND_ANALYSIS.md
§1) indicted one account wearing several client shapes, and the operator's
direction is to mirror the one client the accounts are actually used
through — the standalone Antigravity CLI (`agy`), torn down from the
official v1.5.1 binary with REA (§13 of the analysis doc). This change
deletes the profile concept and rebases the identity wholesale:

- One definition: `internal/providers/antigravity_identity.go`.
  `AntigravityUserAgent()` builds the UA in agy's runtime grammar —
  `antigravity/cli/1.5.1 (aidev_client; os_type=<runtime GOOS>;
  arch=<runtime GOARCH>; cl=1012564305; auth_method=consumer)` — with
  os/arch from the process runtime, never a hardcoded foreign OS, and
  the changelist agy 1.5.1's own build metadata carries (the old UA
  was a frozen 1.4.0-era Windows capture, cl=1000494485).
  `AntigravityLoadCodeAssistMetadata()` returns the numeric
  ClientMetadata: ideType 9 (ANTIGRAVITY), pluginType 2 (GEMINI),
  ideVersion "1.5.1", ideName "antigravity", platform derived from the
  same runtime OS/arch. pluginVersion, updateChannel and duetProject
  are omitted — the teardown could not establish their values, and an
  invented value would itself be a fingerprint.
- `internal/providers/antigravity_profile.go` is deleted. A
  `clientProfile` value stored on an old connection stays in the data,
  inert and unread; nothing in the tree resolves it any more.
- Every surface consumes the one definition: chat forwarding and the
  ForwardGemini fallback, project discovery (loadCodeAssist +
  onboardUser), the chat quota lanes (which sent a stray
  `antigravity/1.0.0` UA plus an X-Client pair), the translator's chat
  and image wrappers (the body `userAgent` field now equals the header
  UA, as in agy), media image/search/STT (X-Client-Name /
  X-Client-Version removed — the real CLI sends no such pair; body
  `userAgent` aligned), dashboard usage, weekly overlay and
  subscription info (X-Client pair removed, metadata via the shared
  builder), the dashboard models fetch, and the connection probe.
  Hosts are unchanged (daily vs prod exactly as before).
- The connection probe's antigravity branch now sends the numeric
  metadata body and the CLI UA: fragment 968 kept it byte-identical to
  the JS build, but agy parity supersedes JS parity for this provider.
  The gemini-cli probe branch is untouched, and X-Goog-Api-Client
  stays absent from loadCodeAssist (the real client does not send it
  there).
- The OAuth sign-in handler seeds its client from
  providers.KnownOAuthConfigs instead of a duplicated literal, and the
  scope list gains https://www.googleapis.com/auth/aicode — the
  distinctive scope present in every agy CLI auth flow.
- X-Goog-Request-Reason is resolved and deliberately not sent: in agy
  the header is conditional on a host-set runtime string that stays
  empty in the standalone CLI flow, so the real client effectively
  never sends it (the JS build never sends it either).

Open residual — the OAuth client swap is DEFERRED (operator decision):
agy signs in with its 884354919052 client, while this router still
signs in and refreshes with the pre-rebase 1071006060591 client,
restored as the primary in KnownOAuthConfigs. The design is ready to
apply the moment the 8843 client's secret lands in the source: the
8843 config becomes primary, and the 1071 config becomes a fenced
refresh-rescue fallback in a dual-config refresher — new client first,
exactly one legacy attempt on a client-mismatch rejection (401, or 400
naming invalid_client / invalid_grant), the primary error surfaced if
the rescue also fails — because existing accounts' refresh tokens are
bound to the client that minted them. If a refresh ever failed under
both clients, that account would need a fresh sign-in; nothing else
about its data changes. No dual-config refresher exists until then:
one client, one home.

Remaining teardown unknowns (§7 of the identity spec): the endpoint
selection rule (daily vs prod per surface), the exact sign-in scope
list beyond the confirmed aicode scope, the token file format, and
the updater TTL.
