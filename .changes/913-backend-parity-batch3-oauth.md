# Backend API parity — Batch 3: OAuth family

Third batch of the AxonRouter backend-parity program (see
~/workspace/axon-backend-parity-map.md), routes-first per Ricki's
2026-11-04 sequencing flip. All 10 routes the mechanical diff found
missing in the OAuth family, implemented against Axon's live contracts
(route.js wins over route.ts) in 17 new files under
internal/handlers/oauth/ (batch3_*.go) + registrations in
internal/handlers/router.go:

**Standalone imports**: `POST /api/oauth/codex/import-token` (pasted
ChatGPT access token; claims → connection), `POST
/api/oauth/supergrok/import` (route.ts contract: zod-mirror validation
envelope, refresh exchange, models-endpoint validation, ephemeral
warning), `POST /api/oauth/zed/import` + `GET
/api/oauth/zed/auto-import` (IDE credential store port: settings.json
credentials_url, kv_store system_id, Linux secret-tool / macOS security
/ Windows CredRead), `POST /api/oauth/freebuff/import` + `GET
/api/oauth/freebuff/auto-import` (manicode credentials.json + instance
owner, no-store header) + `POST /api/oauth/freebuff/realtest` (real
codebuff.com session/agent-run/completion probe, probe.ts port),
`POST /api/oauth/xiaomi-mimo/api-key` (sk- key, models-endpoint soft
validation, uid/token dedupe-update) + `GET
/api/oauth/xiaomi-mimo/auto-import` (~/.local/share/mimocode/auth.json
xiaomi field + Desktop passToken attach).

**Generic dispatch**: `GET|POST /api/oauth/{provider}/{action}` —
authorize, start-proxy, poll-status, stop-proxy, ide-status,
device-code, register-session, exchange, poll, manual-code, bulk-jwt,
delegating to the dedicated per-provider handlers/proxy registries and
reshaping to Axon's envelopes. Adds the loopback callback proxies Axon
runs (xAI fixed 56121, trae/windsurf/zed dynamic, xiaomi-mimo ECDH) and
the WorkBuddy device pair (CodeBuddy endpoints on www.workbuddy.ai),
which the shared device switch does not carry. Chi's static-segment
precedence keeps dedicated routes (codex/start-proxy, trae/exchange,
xiaomi-mimo/exchange, …) owning their URLs; the dispatch covers the
rest.

Deviations, deliberate: Batch 3 errors use Axon's flat {error: string}
shape, not the dashboard's nested envelope. Connection ids are
deterministic (shortHash) for the import routes, matching the existing
handlers. cline-free delegates to the cline flow and stores under
cline (its models live under the cline provider in this catalog).
finalizePostConnectValidation has no Go machinery and is not ported.
The authcode exchange delegation carries no codeVerifier (the existing
handler's body has no such field). Poll answers errorDescription: null
(the device layer has no separate description). Host credential reads
(auto-import routes) mirror Axon despite the Docker-only tension
already flagged program-wide.

Verification: `go build ./...` green, go vet clean, no test suites run
(standing rule). Live smoke against a scratch DATA_DIR server: every
route probed; import round-trips read back through GET
/api/connections (codex JWT import + generic-exchange JWT branch,
xiaomi-mimo api-key create + dedupe update, bulk-jwt lines/JSON/
duplicate mix); live device-code (GitHub) and poll error envelope;
proxy start/poll-status/stop for trae/zed/mimo; dedicated-route
precedence confirmed. codebuff.com and www.workbuddy.ai time out from
this VM, so the freebuff import/realtest and workbuddy device-code
happy paths surface the upstream transport failure as Axon's flat 500
— their request shapes are dispatched correctly but could not be
exercised end-to-end here.
