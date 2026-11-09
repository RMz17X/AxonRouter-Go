# Prompt & Chat Script Injection + Playground Implementation Plan

> **Status (2026-11-05): plan document.** Kept as a design record; not a statement of shipped behaviour. The platform has since moved to PostgreSQL-only storage and Docker delivery (phases P0–P5); any file-database-era stack details in this document are obsolete.


> **For Hermes / Operator:** Implementation plan for porting the complete prompt injection and chat script feature from `giienew/9router-pro` into `9router-go` (Golang + Svelte 5) along with a native interactive Playground.

**Goal:** Upgrade `9router-go`'s injection engine from simple prepend-only system prompts into a full-featured Prompt & Chat Script Injection system (System Prepend/Append + Multi-turn Fake History Injection + Key/Model/Global scopes), and add an interactive Playground with Side-by-Side (VS) testing, SSE streaming, refusal heuristics, and wire payload inspection.

**Architecture:**
- **Backend (Go)**: Extend `internal/db/jailbreak.go` and `internal/handlers/chat/jailbreak.go` with zero-allocation wire transforms supporting OpenAI (`messages`), Anthropic (`system` + strict role alternation auto-merge), and Gemini (`system_instruction`). Backward-compatible with existing SQLite presets.
- **Frontend (Svelte 5 + Tailwind)**: Upgrade `web/src/components/JailbreakView.svelte` with System Prompt (Prepend/Append) & Chat Script (Multi-turn turns editor) tabs. Create `web/src/components/PlaygroundView.svelte` with single and side-by-side chat, real-time SSE streaming, refusal heuristic evaluation, and outbound JSON inspection.
- **Design System Parity**: Pure `9router-go` design tokens (`surface`, `border`, `text-main`, `primary`, Lucide/Material icons, `Card`, `Modal`, `Button`, `Toggle`), strictly avoiding external CSS frameworks or heavy dependencies.

---

## Task Inventory

### Task 1: Extend DB Models & Serialization (`internal/db/jailbreak.go`)
- **Objective:** Add `Mode`, `Position`, and `Turns` fields to `JailbreakDoc` while maintaining backward compatibility with existing stored presets.
- **Files:**
  - Modify: `internal/db/jailbreak.go`
  - Test: `internal/db/jailbreak_test.go`
- **Details:**
  - Define `ChatTurn` struct: `{ Role string, Content string }`.
  - Add to `JailbreakDoc`:
    - `Mode string` (`"single"` default, or `"chat"`)
    - `Position string` (`"prepend"` default, or `"append"`)
    - `Turns []ChatTurn` (for chat script mode)
  - Ensure unmarshaling legacy presets defaults `Mode` to `"single"` and `Position` to `"prepend"`.

### Task 2: Implement Multi-Format Injection Engine (`internal/handlers/chat/jailbreak.go`)
- **Objective:** Support both System Prompt (prepend/append) and Chat Script (fake turns) across OpenAI, Claude, and Gemini wire formats with strict role-merging.
- **Files:**
  - Modify: `internal/handlers/chat/jailbreak.go`
  - Create: `internal/handlers/chat/jailbreak_inject_test.go`
- **Details:**
  - `injectSystemPrompt(body map[string]any, prompt string, position string)`:
    - OpenAI format: Insert or append to `messages` with role `system`.
    - Claude format: If top-level `system` exists, prepend or append with `\n\n`. If slice of content blocks, prepend/append text block.
    - Gemini format: Handle `system_instruction.parts`.
  - `injectChatTurns(body map[string]any, turns []db.ChatTurn)`:
    - Inject fake turns right before the final user message.
    - Normalize & merge consecutive turns with identical roles so Anthropic API doesn't throw 400 Bad Request.
  - Idempotency guard: Avoid double injection if payload already contains the exact prompt.

### Task 3: Update Dashboard API Handlers (`internal/handlers/dashboard/jailbreak_handler.go`)
- **Objective:** Update `/api/jailbreak/preview` to correctly reflect Chat Script turns in the preview output.
- **Files:**
  - Modify: `internal/handlers/dashboard/jailbreak_handler.go`
  - Modify: `internal/handlers/dashboard/jailbreak_handler_test.go` (or integration test)
- **Details:**
  - Update `previewOutboundBody` to execute `injectChatTurns` when `doc.Mode == "chat"`.
  - Return `{ injected: true, mode: doc.Mode, position: doc.Position, messages: [...] }`.

### Task 4: Upgrade Frontend Persona / Prompt Injection View (`web/src/components/JailbreakView.svelte`)
- **Objective:** Provide an intuitive UI for editing both Single System Prompts (Prepend/Append) and Chat Scripts (fake turn sequences).
- **Files:**
  - Modify: `web/src/api/client.ts` (types: `ChatTurn`, `JailbreakDoc` updates)
  - Modify: `web/src/components/JailbreakView.svelte`
- **Details:**
  - Add Mode Switch: **System Prompt** (`single`) vs **Chat Script** (`chat`).
  - In System Prompt mode:
    - Radio pills: `Prepend` (before client system prompt) vs `Append` (after client system prompt).
    - Token estimation indicator (`~N tokens`).
  - In Chat Script mode:
    - Multi-turn builder: List of turns (`User` / `Assistant`).
    - Buttons: Add Turn (auto-alternates role), Delete Turn, Content textarea.
    - Preview badge showing count of injected turns.
  - Update Payload Preview Modal to show fake turns formatted cleanly.

### Task 5: Build Interactive Playground (`web/src/components/PlaygroundView.svelte`)
- **Objective:** Implement a full playground supporting single chat and side-by-side (VS) model comparison, live SSE streaming, prompt injection testing, and refusal detection.
- **Files:**
  - Create: `web/src/components/PlaygroundView.svelte`
  - Modify: `web/src/lib/router.ts` (add route `/dashboard/playground`)
  - Modify: `web/src/lib/nav.ts` (add `playground` to navigation group `main`)
  - Modify: `web/src/App.svelte` (register `playground` view)
- **Details:**
  - **Header Controls**:
    - Model Selectors (Single model, or Model A vs Model B in VS mode).
    - Injection Preset Picker: Test with any saved Persona/Jailbreak preset or none.
    - Mode Switch: Single Chat vs Side-by-Side (VS).
    - Quick Parameter controls: Temperature, Max Tokens, System Prompt override.
  - **Chat Area**:
    - Clean message history (User bubble, Assistant response bubble).
    - Real-time SSE streaming from `/v1/chat/completions`.
    - Markdown rendering with syntax highlighting for code blocks.
    - Metrik badges per response: Latency (ms), output tokens, speed (tok/s).
    - **Refusal Heuristic Badge**: Auto-evaluates response header against refusal signatures:
      - `ANSWERED` (green pill)
      - `REFUSED` (red pill)
      - `UNCLEAR` (yellow pill)
  - **"Inspect Wire Payload" Modal**:
    - View raw outgoing JSON payload to verify that system prompts and chat turns were injected as expected.

### Task 6: Build, Vet & Verification
- **Objective:** Verify everything builds cleanly with zero errors, unit tests pass, and container runs without regressions.
- **Verification Commands:**
  - Go unit tests: `go test ./internal/db/... ./internal/handlers/chat/... -v`
  - Svelte check: `make vet-svelte`
  - Frontend build: `cd web && bun run build`
  - Docker container rebuild: `docker compose build && docker compose up -d`
  - Smoke test: Access `http://100.81.73.83:3177/dashboard/playground` and verify both injection and playground streaming work end-to-end.

---
