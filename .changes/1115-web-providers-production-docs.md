---
type: changed
---

Production wrap-up for the two new web providers: `docs/CLAUDE_WEB_PROVIDER_SPEC.md` and `docs/KIMI_WEB_PROVIDER_SPEC.md` each gain a section 10 recording the shipped implementation — file map, wire deviations from the spec text (the claude.ai host correction, the completion body's `parent_message_uuid` and `model` fields, kimi's in-process token-cache limitation), the Chroma sync contracts, and the verification evidence — with the happy-path live proof against real accounts explicitly marked as the one item still outstanding. No behavior changes. The pre-production gate over the touched trees: full `go vet` (default and integration tags) clean; executor and dashboard test suites pass; the providers suite shows only the two failures that predate these providers on the base tree (gemini-web/deepseek-web catalog parity from the other lineage), with zero complaints about claude-web or kimi-web.
