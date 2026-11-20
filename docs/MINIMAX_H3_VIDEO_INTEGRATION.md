# MiniMax H3 Video Integration — Handoff Spec

Status: **implemented on `mmg` / `minimax-code-global`** (2026-11-20). This
document remains the API contract and the record of the original design; the
operator later directed that video ride the existing global MiniMax Code
credential instead of a new provider. See §7 for the shipped shape.

## 1. Goal

Expose MiniMax's H3 video generation through AxonRouter-Go as a distinct,
isolated provider capability:

- create a video job from a text prompt and optional reference media;
- poll job status by `task_id`;
- stream the finished MP4 back to the caller;
- log usage and respect 429 / upstream failure semantics.

Do not merge this into another provider's model catalog. AGENTS.md §3
(`AGENTS.md:60-94`) forbids cross-provider aliasing and model hijacking. The
original recommendation was a dedicated provider; the shipped implementation
is scoped to `mmg` only (§7), with no other provider inheriting H3.

## 2. Official MiniMax API contract

Sources: [MiniMax Code H3 Max](https://agent.minimax.io/docs/code/agents/h3-max),
[H3 video guide](https://platform.minimaxi.com/docs/guides/video-generation),
[create task API](https://platform.minimax.io/docs/api-reference/video-generation-v2-create),
[M Plan](https://platform.minimax.io/docs/m-plan/intro).

### Endpoints

| Action | Request | Success shape |
|---|---|---|
| Create | `POST {base}/v2/video_generation` | `{ "task_id": "..." }` |
| Poll | `GET {base}/v2/query/video_generation/{task_id}` | `{ "task": { "id", "status", "content": { "url" }, ... } }` |
| Download | `GET <task.content.url>` | MP4 bytes |

Auth: `Authorization: Bearer <API key>`, `Content-Type: application/json`.
Base URLs: `https://api.minimax.io` (global), `https://api.minimax.cn` (China).
The two sites issue separate credentials and must not be aliased.

### Models and limits

| Model | Resolution | Duration |
|---|---|---|
| `MiniMax-H3` | 768P, 2K | 4–15 s |
| `MiniMax-H3-Highspeed` | 480P, 768P | 4–15 s |
| `MiniMax-H3-Lite` | 480P, 768P | 4–15 s |
| `MiniMax-H3-Max` | 480P, 768P | 5–15 s (no 2K) |

`generate_audio` defaults to `true`. Prompt is required, max 7000 characters.
Reference media must be public http(s) URLs: ≤9 images, ≤3 videos, ≤3 audio
files, ≤12 total. Frame inputs (`first_frame` / `last_frame`) and reference
inputs (`reference_image` / `reference_video` / `reference_audio`) are mutually
exclusive in one request. Request body limit is 64 MB.

Create body example:

```json
{
  "model": "MiniMax-H3",
  "content": [
    {"type": "text", "text": "A paper boat drifts across a calm pond."},
    {"type": "image_url", "image_url": {"url": "https://.../first.png"},
     "role": "first_frame"}
  ],
  "duration": 5,
  "resolution": "768P",
  "ratio": "16:9",
  "generate_audio": false
}
```

Poll terminal states: `succeeded`, `failed`, `cancelled`. Non-terminal states
must be polled with backoff; upstream recommends ~10 s intervals. HTTP 429 must
honour `Retry-After`; treat 5xx as retryable with bounded backoff, but never
re-send a create after a network error or 5xx — the job may already exist
upstream (`video_handlers.go:15-17`).

Plan gating: M Plan **Go** does not include a video model; Explore/Build include
H3. Other cases are credit / pay-as-you-go. MiniMax Code app itself supports
H3 and H3 Max via its video plugin. Do not assume an `minimax-code-global`
OAuth chat token automatically carries video credits; verify against the
account/plan before relying on it.

## 3. Existing AxonRouter-Go integration points

| Concern | Location | Note |
|---|---|---|
| Provider config | `internal/providers/providers.go` | `ProviderConfig` at `:10` (`VideoURL` at `:21`, `ImageURL` at `:18`), `KnownProviders` map at `:384`. `xai` (`:597-603`) and `openrouter` (`:495-507`) carry `VideoURL`; `minimax` / `minimax-code` / `minimax-code-global` are chat-lane entries at `:939-983`. |
| Provider metadata | `internal/providers/provider_registry.go` | `ProviderMeta` at `:11`, `ServiceKinds` at `:32`; `minimax` at `:1405` has `llm,image,tts,webSearch` — **no `video`**. `category.go:280` already defines the `video` kind. |
| Model catalog | `internal/providers/registry_models.go` | `ProviderModels` at `:17`, `ProviderModelKinds` at `:297`; existing MiniMax rows at `:215-218`. Add H3 model ids/kinds here. |
| Aliases | `internal/providers/aliases.go:4`, `registry_aliases.go:63-64` | `mm`→`minimax` (`:74`), `mmc`→`minimax-code` (`:79`), `mmg`→`minimax-code-global` (`:80`). Never alias the video provider to `mm`/`mmg`. |
| Executor registration | `internal/proxy/executor/init.go` | `Register(provider, factory)`; MiniMax chat paths at `:59`, `:63`, `:123-124`. Register only the dedicated video executor. |
| Connection selection | `internal/handlers/chat/connections.go` | `GetBestConnectionWithContext` `:264`, `getBestConnectionWithContext` `:273-333`, `ExtractAPIKey` `:1192`, `GetProviderConfig` `:719-803`; pinned connection must be same provider (`:295-298`). |
| Generic media lane | `internal/handlers/media/media.go:1119-1250`, `1345-1380` | Dispatch table and target-URL switch; video branch at `:1353-1362` uses `providerCfg.VideoURL`. `runwayml` / `vertex-partner` have video metadata but no `VideoURL`, so they fall to the generic lane. |
| Connection model | `internal/models/types.go:17`, `internal/handlers/shared/types.go:27` | `ProviderConnection` holds `Provider`, `AuthType`, `Data`; `ConnectionData` exposes `APIKey`, `BaseURL`, `ProviderSpecificData`. |
| Video core | `internal/handlers/media/video_core.go` | `videoConfigFor` (`45-53`), `videoBaseURL` (`55-76`), `buildVideoUpstreamURL` (`78-95`), `videoProxy` (`136-166`), `videoFetch` (`183-274`). Scope comment at `3-18`: only providers with `VideoURL` enter this core. |
| Create/poll/content handlers | `internal/handlers/media/video_handlers.go` | Pinning via `x-axonrouter-connection-id` (`3-13`, `250-275`); rotation only on 401/403/429 (`15-17`); usage logging `235-248`. |
| Route registration | `internal/handlers/media/media.go:686-716` | `POST /v1/videos/{generations,edits,extensions}`; GET handlers for status/content. |
| Existing name collision | `internal/handlers/media/uc_web.go:133` | `ucVideoCatalog` already lists `minimax-h3` for the **uc-web** provider. That is a different upstream; do not route it to MiniMax or alias them. |
| Provider isolation tests | `internal/providers/minimax_code_test.go:12-88` | Existing pattern proving mcode sites and API-key `minimax` stay separate. Mirror this for video. |
| Changelog rule | `AGENTS.md:665-673`, `.changes/README.md` | Add `.changes/<pr>-minimax-h3-video.md`; never edit `CHANGELOG.md`. |

## 4. Recommended implementation shape

The shape below was the original recommendation. It was superseded by the
operator decision recorded in §7; the contract in §2 and the acceptance
criteria that still apply are unchanged.

1. **New provider identity**, e.g. `minimax-video` (and `minimax-video-cn` only
   if the China site is required). Add all of:
   - `ProviderConfig` in `internal/providers/providers.go` (`VideoURL` at `:21`, `KnownProviders` at `:384`);
   - `ProviderMeta` with `ServiceKinds: ["video"]` in `internal/providers/provider_registry.go` (cf. `xai` at `:456`, current `minimax` at `:1405`);
   - model rows in `internal/providers/registry_models.go` (MiniMax rows `:215-218`);
   - an alias in `internal/providers/aliases.go` only if genuinely needed — never map it to `mm`/`mmg`;
   - executor registration in `internal/proxy/executor/init.go`.

   Do not attach it to `minimax-code` / `minimax-code-global` or `uc-web`.

2. **Dedicated adapter** `internal/handlers/media/minimax_video.go`, following
   the provider-specific-file rule in `AGENTS.md:87-93` (cf.
   `antigravity_image.go`). Reuse the existing connection pinning and rotation
   behaviour, but override three things:
   - create URL: `{base}/v2/video_generation` instead of `{base}/{action}`;
   - poll URL: `{base}/v2/query/video_generation/{task_id}`;
   - content URL: the `content.url` returned by the poll response.

   Do not force this into `buildVideoUpstreamURL`'s `/v1/videos` shape without
   an explicit MiniMax branch; that would corrupt other providers' routing.

3. **Payload translation** from the gateway's existing `/v1/videos/generations`
   body into MiniMax's `content[]` array. Validate model/resolution/duration
   at the boundary using named constants (AGENTS.md §4.C enum rule), not
   repeated raw string checks. Reject frame+reference mixtures with a 400.

4. **Credentials**: read the API key from the selected `ProviderConnection`
   through the existing `shared.ConnectionData` path. If using the
   `minimax-code-global` OAuth lane is desired, add it as an explicit, tested
   credential mode; do not silently reuse a chat token.

5. **Job state**: the current video core has no job store — it pins the
   creating connection in a header. Keep that design for the first version.
   If a store is added, bound it with a TTL and size limit (`AGENTS.md:193`).

6. **HTTP hygiene**: reuse a pooled `*http.Client` (`AGENTS.md:115-116`),
   propagate `r.Context()`, cap buffered bodies (`videoBodyReadCap`), stream
   MP4 content, and redact bearer tokens in errors (`sanitizeVideoSecrets`).

7. **Usage + docs**: call the `logVideoUsage` pattern; update `docs/API.md`
   and/or `wiki/api.md` only for newly exposed endpoints; add the changelog
   fragment described above.

## 5. Acceptance criteria

- `minimax-h3` on `uc-web` still routes to uc-web; no cross-provider alias.
- `minimax` (API key), `minimax-code`, `minimax-code-global` remain distinct.
- `mmg` exposes the four H3 models with `kind: "video"` in `registry_models.go`,
  the web catalog, and a `VideoURL` on `minimax-code-global`; no new provider ID.
- Dashboard still shows `minimax` without `video`; `mm` / `mmc` / `mmg` aliases
  are unchanged. Adding H3 under `mmg` is the deliberate operator decision.
- Create → poll → content works with a mock upstream (no live credits needed).
- 429 honours `Retry-After`; 401/403 rotates once; 5xx/network never duplicates
  a create; API keys and tokens never appear in responses or logs.
- Model/resolution/duration validation rejects `2K` on `H3-Max`, out-of-range
  durations, and mixed frame/reference payloads.
- Table-driven `testing.T` tests (`AGENTS.md §5`) in
  `internal/handlers/media/` and `internal/providers/`.
- `go test ./internal/handlers/media ./internal/providers` and `graft build`
  pass; `.changes/` fragment present; no `CHANGELOG.md` edit; no remote push.

## 6. Notes for the next session

- Work in `/workspaces/AxonRouter-Go` (branch `main`, currently ahead 2 of
  `origin/main`). Check `git status -sb` and drift before editing.
- Do not re-add the reverted Petanian prototype: there is no H3 worker, CLI
  command, or journal path in Petanian now, by explicit operator instruction.
- Upstream behavioural source of truth is `decolua/9router` (AGENTS.md §1).
  MiniMax H3 is a new capability, not a parity port; record that divergence
  in the `.changes/` fragment.

## 7. Shipped shape (operator decision, 2026-11-20)

The operator directed video generation onto the existing `mmg` /
`minimax-code-global` lane rather than a new provider. The implementation is
therefore deliberately different from section 4:

- `VideoURL` was added to the `minimax-code-global` registry entry; H3 models
  and `kind: "video"` rows were added to `registry_models.go` and
  `web/src/lib/models.ts`. No new provider ID, alias, or executor was created.
- `ServiceKinds` on `minimax-code-global` now includes `video` alongside `llm`;
  model-level filters still drive `/v1/models/{kind}`, so this is a dashboard
  metadata correction, not a routing change.
- `internal/handlers/media/minimax_code_global_video.go` owns payload
  translation, resolution/duration limits, and task-state parsing;
  `minimax_code_global_video_handler.go` wires create/poll/content into the
  existing video core with the `mmg` credential.
- The global MiniMax Code agent host still serves chat; video goes to
  `https://api.minimax.io/v2/video_generation`. `minimax-code` (China),
  API-key `minimax`, and UC Web remain untouched.
- Delegated token minting and per-site credential modes are not implemented:
  the selected connection's existing bearer token is used, and `mmg/<model>`
  is rejected by upstream if the account's plan lacks video.
