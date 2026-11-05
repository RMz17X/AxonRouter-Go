# 943 — Integration suite: dashboard writes authenticate as the operator

Council round-3 follow-up (test-side only; no product code changed). The
round-3 release-gate suite run on the pre-rename tree ended 173 PASS /
77 FAIL / 6 SKIP, and all 77 failures were one mechanism: the round-3
write sweep (`RequireOperatorForWrites`) returns 401 on dashboard WRITE
routes called with a bare engine API key, and the suite harness
authenticated every Env-client request with the one seeded engine key.
The sweep is the intended contract — an engine key spends on the data
plane, it does not administer the gateway — so the harness, not the
product, was wrong.

## Mechanism

`internal/integration/harness_test.go`:

- `Env` gains `OperatorToken`, derived once at construction while the
  Env's own `DATA_DIR` is the pinned one: `auth.CLIToken()` reads the
  machine-id / cli-secret files under that directory, and the
  in-process server derives the same value when validating the header.
  Capturing it per Env keeps the token correct even after a later test
  re-pins the process env.
- New call variants `GetOp` / `PostOp` / `PutOp` / `DeleteOp`: they send
  the CLI token (`x-9r-cli-token`) INSTEAD of the engine key — the same
  caller shape as the session-cookie calls the suite already makes
  (`sessionOnly` in relay_deploy_test.go). The CLI token is a
  first-class operator credential (`CallerIsDashboardPrivileged` admits
  session, CLI token, or a login-disabled install), and it is already
  how this suite authenticates operator-only calls (TestDashboardAuthGate
  and six other files present it per-call). A password login flow was
  not used: it would require seeding a bcrypt password hash and a
  cookie jar per Env for no extra contract coverage — the session class
  is already pinned by TestDashboardAuthGate and the relay-deploy tests.
- Plain `Get` / `Post` / `Put` / `Delete` are unchanged: the engine key
  remains the credential for `/v1/*` lanes and for any call whose
  subject is the key itself, including the deliberate key-refused
  assertions (e.g. the unauthorised prune caller in
  ledger_prune_quota_test.go, the anonymous storage-maintenance calls,
  TestDashboardAuthGate's caller-class table), which keep their exact
  pre-sweep shapes.

## Call sites

87 dashboard write call sites across 24 test files moved to the Op
variants — the administration/setup writes of the previously failing
families: key create/rotate/policy/allowlist/quota-reset, combos and
smart-rule writes, model-test probes (`POST /api/models/test`), settings
PUTs, connection and proxy-pool writes, provider-node limits and share
links. Reads were left as key calls (the sweep does not gate them), and
already-operator calls (explicit CLI-token opts in provider_overrides,
console_account_log, ledger_prune) were left untouched.

## Relay-deploy contract (equal specificity)

`TestRelayDeployRoutesRejectEngineKeyOnEmptyToken` encoded the
pre-sweep letter: the key was admitted and the handler's empty-token
validation answered 400. Post-sweep both halves are asserted: the
engine key on `POST /api/proxy-pools/deno-deploy` now gets the gate's
flat 401 `{"error":"Unauthorized"}`, and an operator caller with the
same empty payload reaches the handler and gets its 400
"Organization domain is required". The security intent — an engine key
must not deploy — holds, strengthened.

## Verification

On scratch DB `router9_harnessfix` (staging PG), `-tags integration`,
`-count=1`, targeted `-run` families (the full suite runs once on the
merged release tree, per the release plan):

- Keys + settings-schedule + system-prompt + rate-limiter families: ok (61 s).
- Model-test probes + combos/smart-rules + allowlist families: ok (151 s).
- Connections/proxy-pools + provider-nodes + thinking families, plus a
  previously-passing control sample — TestDashboardAuthGate,
  chat-lane path parity, TestJailbreakRoutesAreMounted, guardrails,
  storage maintenance, anonymous prune: ok (137 s).
- Relay-deploy pair re-run with the rewritten test: ok.
- `go build ./...`, `go vet ./...`, `go vet -tags integration ./...`:
  green. gofmt clean on all changed files (pre-existing drift in three
  untouched files — force_fallback, gemini_tool_id_178,
  opencode_zen_usage — left as found).
