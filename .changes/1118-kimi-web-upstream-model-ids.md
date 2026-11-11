---
type: fixed
---

kimi-web chats could never succeed: every completion call carried the client-facing model name (`kimi-latest`, `kimi-search`, `kimi-k2.5`) as the upstream `model` field, and kimi.moonshot.cn rejects anything outside `[kimi k1 k1.5 k2 k1.5-thinking]` with a 422 validation error (`Model必须是[...]中的一个`). The spec's section 7 table named upstream ids that were never exercised against a real account — the same failure class as the claude-web spec's unresolvable host. The auth leg was never broken: with the 12 real connections synced on 2026-11-11 the refresh exchange succeeded and requests reached the completion endpoint; only the model id was refused. The executor's route map now translates: `kimi-latest` → `kimi`, `kimi-search` → `kimi` (with `use_search`), `kimi-k2.5` → `k2`, `kimi-web` → `kimi`; the spec table is corrected to match. Proven live after the fix: real chats through all three public models return 200 from the upstream.
