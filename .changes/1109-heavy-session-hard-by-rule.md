---
type: fix
---

A heavy working session now takes the hard tier by rule, without a router evaluation. The smart router judges only the last few user turns, and in harness traffic (coding agents, OpenBot) those turns are scaffolding — instruction blocks, environment updates, system notifications — not the task: a live audit found a 70K–383K-token agent session pinned to the easy tier while the heavy-session guard silently undid the easy lead on every turn, so the recorded verdict and the badge contradicted the model actually serving. When the locally computed session signals say heavy (tools in use, or the conversation past the size cap), the gateway leads with the hard pool, pins its lead model, and records the decision as hard with source "heavy-session". The router now only judges light sessions, where the latest turn really is the task. The heavy-session demotion guard stays in place as the safety net for every other path.
