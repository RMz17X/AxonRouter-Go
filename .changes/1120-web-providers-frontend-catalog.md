---
type: fixed
---

The provider pages for kimi-web and claude-web rendered an empty model list. The backend was never the problem — the registry rows, routing, and /v1/models carried both providers from their first releases — but the provider detail view renders its model section from the frontend's static catalog (`web/src/lib/models.ts`, generated from the UPSTREAM provider registry), and that catalog had no entries for the two Go-only web providers: no alias rows, no model lists, so `getModelsByProviderId` resolved to an empty array. Sibling web providers (deepseek-web, gemini-web) appear because upstream has them; claude-web and kimi-web exist only in this port. Both are now listed with their aliases (`cweb`, `kweb`) and full model catalogs. Hazard recorded: models.ts is generated from the upstream registry, so a regeneration would drop these local-only entries again — they must be re-applied, or the generator gains a local overlay, before any regeneration ships.
