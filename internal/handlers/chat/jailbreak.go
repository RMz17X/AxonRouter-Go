package chat

import (
	"context"
	"strings"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/translator"
)

// resolveJailbreak returns the injection document for a request, or nil when
// nothing should be injected. ids are the candidate model ids in specificity
// order — the id the client sent first, then the ids the request is forwarded
// to — and the resolver applies exact bindings, keyword rules, and the
// exclude lists (see internal/db/jailbreak_keywords.go for the precedence).
// An empty document means pass-through, exactly like a toggle left off.
func (h *ChatHandler) resolveJailbreak(ids ...string) *db.JailbreakDoc {
	if h.Repo == nil {
		return nil
	}
	doc, err := h.Repo.ResolveJailbreakDoc(ids...)
	if err != nil {
		log.Warn("jailbreak", "doc lookup failed", "ids", ids, "error", err)
		return nil
	}
	if doc == nil || !doc.HasContent() {
		return nil
	}
	return doc
}

// applyJailbreak mutates the outbound request body for a request that resolves
// to a document and returns a context carrying the Gemini-safety flag for the
// forward chain. Called before the body is marshalled, so the prompt-cache hash
// always reflects the injected prompt. Every forward path calls this: single
// chat, Claude single, combo fallback, Claude combo, fusion panel, fusion
// judge. requestedModel is the id the client sent — a model id, or the combo
// name, which arms every leg of that combo. legIDs are the ids the request is
// actually forwarded to, so a keyword such as "deepseek" covers a combo leg
// like providerx/deepseek-4-pro without arming that id.
func (h *ChatHandler) applyJailbreak(ctx context.Context, body map[string]any, requestedModel string, legIDs ...string) context.Context {
	// A playground request carries its own prompt decision on the context
	// (playground.go): a session prompt or a chosen preset replaces the
	// standing per-model configuration outright, and a suppression-only
	// override disables it. Either way the standing resolver is never
	// consulted, so the two can never stack.
	if ov, ok := playgroundPromptFromContext(ctx); ok {
		if ov == nil || ov.doc == nil || !ov.doc.HasContent() {
			return ctx
		}
		return h.applyJailbreakDoc(ctx, body, ov.doc, requestedModel)
	}
	doc := h.resolveJailbreak(append([]string{requestedModel}, legIDs...)...)
	if doc == nil {
		return ctx
	}
	return h.applyJailbreakDoc(ctx, body, doc, requestedModel)
}

// applyJailbreakDoc injects one resolved document into the outbound body and
// returns the context carrying its Gemini-safety flag. Shared by the standing
// resolver above and the playground override, so both ride the exact same
// injection mechanics (placement per wire format, usage counter, safety flag).
func (h *ChatHandler) applyJailbreakDoc(ctx context.Context, body map[string]any, doc *db.JailbreakDoc, requestedModel string) context.Context {
	if doc.Mode == "chat" && len(doc.Turns) > 0 {
		injectChatTurns(body, doc.Turns)
		log.Info("jailbreak", "applied_chat_script", "model", requestedModel,
			"turns", len(doc.Turns), "safety_off", doc.GeminiSafety)
	} else if doc.Prompt != "" {
		injectSystemPrompt(body, doc.Prompt, doc.Position)
		log.Info("jailbreak", "applied_system_prompt", "model", requestedModel,
			"position", doc.Position, "prompt_bytes", len(doc.Prompt), "safety_off", doc.GeminiSafety)
	}
	// Best-effort usage counter for the dashboard's per-preset hit count. A
	// failed write must not fail a request that is already being injected.
	// The nil guard keeps the playground override path safe on a handler
	// built without a repository; the standing path only reaches here with
	// a resolved doc, which already implies a repository.
	if h.Repo != nil {
		if err := h.Repo.RecordJailbreakUsage(doc.ID); err != nil {
			log.Warn("jailbreak", "usage counter failed", "preset", doc.ID, "error", err)
		}
	}
	if doc.GeminiSafety {
		ctx = translator.WithGeminiSafety(ctx, true)
	}
	return ctx
}

// jailbreakLegIDs returns the two ids a forwarded leg answers to: the
// "provider/model" form the dashboard and /v1/models use, and the bare model
// the upstream body carries. A keyword or exclude matches either.
func jailbreakLegIDs(provider, model string) []string {
	return []string{provider + "/" + model, model}
}

// prependSystemPrompt injects the configured prompt ahead of the original
// content. Two wire formats reach here: OpenAI (system role in "messages") and
// Claude (top-level "system", string or block array). Both get the prompt
// first, original content intact after it.
func prependSystemPrompt(body map[string]any, prompt string) {
	injectSystemPrompt(body, prompt, "prepend")
}

// toAnySlice converts []any or []map[string]any into []any safely.
func toAnySlice(v any) []any {
	switch s := v.(type) {
	case []any:
		return s
	case []map[string]any:
		res := make([]any, len(s))
		for i, m := range s {
			res[i] = m
		}
		return res
	default:
		return nil
	}
}

// injectSystemPrompt injects the configured prompt into the body at the requested
// position ("prepend", "append", or "replace"). Prepend and append keep the
// client's own system content and place the prompt before or after it; replace
// discards the client's system content so the prompt is the only system content.
func injectSystemPrompt(body map[string]any, prompt string, position string) {
	if prompt == "" || body == nil {
		return
	}
	if position != "append" && position != "replace" {
		position = "prepend"
	}

	// 1. Claude top-level system field
	if raw, ok := body["system"]; ok {
		switch v := raw.(type) {
		case string:
			if strings.Contains(v, prompt) {
				return // idempotent
			}
			if position == "replace" {
				body["system"] = prompt
			} else if position == "append" {
				body["system"] = v + "\n\n" + prompt
			} else {
				body["system"] = prompt + "\n\n" + v
			}
			return
		case []any:
			for _, item := range v {
				if m, ok := item.(map[string]any); ok {
					if text, ok := m["text"].(string); ok && strings.Contains(text, prompt) {
						return // idempotent
					}
				}
			}
			block := map[string]any{"type": "text", "text": prompt}
			if position == "replace" {
				body["system"] = []any{block}
			} else if position == "append" {
				body["system"] = append(v, block)
			} else {
				body["system"] = append([]any{block}, v...)
			}
			return
		}
	}

	// 2. Gemini top-level system_instruction field
	if raw, ok := body["system_instruction"]; ok {
		if si, ok := raw.(map[string]any); ok {
			if parts, ok := si["parts"].([]any); ok {
				for _, p := range parts {
					if pm, ok := p.(map[string]any); ok {
						if txt, ok := pm["text"].(string); ok && strings.Contains(txt, prompt) {
							return
						}
					}
				}
				part := map[string]any{"text": prompt}
				if position == "replace" {
					si["parts"] = []any{part}
				} else if position == "append" {
					si["parts"] = append(parts, part)
				} else {
					si["parts"] = append([]any{part}, parts...)
				}
				return
			}
		}
	}

	// 3. OpenAI / standard messages format
	rawMsgs, ok := body["messages"]
	if !ok {
		body["messages"] = []any{map[string]any{"role": "system", "content": prompt}}
		return
	}

	msgs := toAnySlice(rawMsgs)
	if msgs == nil {
		return
	}

	// Idempotency check across existing messages
	for _, m := range msgs {
		msgMap, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msgMap["role"].(string)
		if role == "system" || role == "developer" {
			if content, ok := msgMap["content"].(string); ok && strings.Contains(content, prompt) {
				return
			}
		}
	}

	newSysMsg := map[string]any{"role": "system", "content": prompt}

	if position == "replace" {
		// Drop every system/developer message; the persona message takes the
		// slot of the first one removed (index 0 when there were none), so the
		// remaining messages keep their relative order.
		kept := make([]any, 0, len(msgs))
		insertIdx := -1
		for _, m := range msgs {
			if msgMap, ok := m.(map[string]any); ok {
				role, _ := msgMap["role"].(string)
				if role == "system" || role == "developer" {
					if insertIdx < 0 {
						insertIdx = len(kept)
					}
					continue
				}
			}
			kept = append(kept, m)
		}
		if insertIdx < 0 {
			insertIdx = 0
		}
		var newMsgs []any
		newMsgs = append(newMsgs, kept[:insertIdx]...)
		newMsgs = append(newMsgs, newSysMsg)
		newMsgs = append(newMsgs, kept[insertIdx:]...)
		body["messages"] = newMsgs
		return
	}

	if position == "append" {
		// Find index after the last system/developer message
		insertIdx := 0
		for i, m := range msgs {
			if msgMap, ok := m.(map[string]any); ok {
				role, _ := msgMap["role"].(string)
				if role == "system" || role == "developer" {
					insertIdx = i + 1
				}
			}
		}
		var newMsgs []any
		newMsgs = append(newMsgs, msgs[:insertIdx]...)
		newMsgs = append(newMsgs, newSysMsg)
		newMsgs = append(newMsgs, msgs[insertIdx:]...)
		body["messages"] = newMsgs
		return
	}

	// Default "prepend": insert at the very beginning (index 0)
	body["messages"] = append([]any{newSysMsg}, msgs...)
}

// injectChatTurns injects multi-turn scripted fake conversation history into the request.
// It places the turns right before the real user conversation and auto-merges consecutive identical
// roles to avoid strict provider rejections (e.g. Anthropic consecutive role errors).
func injectChatTurns(body map[string]any, turns []db.ChatTurn) {
	if body == nil || len(turns) == 0 {
		return
	}

	var clean []db.ChatTurn
	for _, t := range turns {
		role := strings.ToLower(strings.TrimSpace(t.Role))
		content := strings.TrimSpace(t.Content)
		if (role == "user" || role == "assistant") && content != "" {
			clean = append(clean, db.ChatTurn{Role: role, Content: content})
		}
	}
	if len(clean) == 0 {
		return
	}

	rawMsgs, ok := body["messages"]
	if !ok {
		var msgs []any
		for _, t := range clean {
			msgs = append(msgs, map[string]any{"role": t.Role, "content": t.Content})
		}
		body["messages"] = msgs
		return
	}

	msgs := toAnySlice(rawMsgs)
	if msgs == nil {
		return
	}

	// Find the insertion point: after any initial system/developer messages
	insertIdx := 0
	for insertIdx < len(msgs) {
		m, ok := msgs[insertIdx].(map[string]any)
		if !ok {
			break
		}
		role, _ := m["role"].(string)
		if role != "system" && role != "developer" {
			break
		}
		insertIdx++
	}

	// Check idempotency: if turns are already at insertIdx
	alreadyInjected := len(clean) <= len(msgs)-insertIdx
	for i := 0; alreadyInjected && i < len(clean); i++ {
		m, ok := msgs[insertIdx+i].(map[string]any)
		if !ok {
			alreadyInjected = false
			break
		}
		r, _ := m["role"].(string)
		c, _ := m["content"].(string)
		if r != clean[i].Role || c != clean[i].Content {
			alreadyInjected = false
			break
		}
	}
	if alreadyInjected {
		return
	}

	// Prepare block of turns
	var block []map[string]any
	for _, t := range clean {
		block = append(block, map[string]any{"role": t.Role, "content": t.Content})
	}

	// Check consecutive role between the last injected turn and the first real user message
	if insertIdx < len(msgs) {
		firstReal, ok := msgs[insertIdx].(map[string]any)
		lastInjected := block[len(block)-1]
		if ok {
			realRole, _ := firstReal["role"].(string)
			lastRole, _ := lastInjected["role"].(string)
			if realRole == lastRole {
				// Same role! Merge content with double newline to prevent provider errors
				realContent, _ := firstReal["content"].(string)
				lastInjected["content"] = lastInjected["content"].(string) + "\n\n" + realContent
				// Remove the real message at insertIdx as it's merged into lastInjected
				msgs = append(msgs[:insertIdx], msgs[insertIdx+1:]...)
			}
		}
	}

	// Insert block at insertIdx
	var newMsgs []any
	newMsgs = append(newMsgs, msgs[:insertIdx]...)
	for _, b := range block {
		newMsgs = append(newMsgs, b)
	}
	newMsgs = append(newMsgs, msgs[insertIdx:]...)
	body["messages"] = newMsgs
}
