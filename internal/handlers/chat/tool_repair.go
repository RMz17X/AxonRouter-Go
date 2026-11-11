package chat

import (
	json "axonrouter/proxy/internal/fastjson"
	"fmt"
)

// repairToolCallIDsInMap ensures every role: "tool" message pairs with a
// tool_call declared somewhere in the same body (PR #4090).
// Strict upstreams (NVIDIA, OpenAI, Antigravity) reject requests with
// HTTP 400 when tool_call_id is missing — and strict pairing validators
// (MiniMax: "tool result's tool id(x) not found (2013)") reject a
// tool_call_id that no declared tool_call carries. The previous repair
// stamped a fabricated `call_tool_<index>` id onto orphan tool results,
// which cured the missing-id rejection but manufactured exactly the
// dangling-id rejection: no assistant message ever declared that id.
// An orphan tool result (no declared call to pair with) is therefore
// converted into a user message: its content survives, and the body
// satisfies both classes of validator.
func repairToolCallIDsInMap(body map[string]any) {
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return
	}
	// Pass 1: stamp synthetic ids onto id-less declared calls (those pair
	// correctly — the call side carries the same id) and collect every
	// declared id in the body.
	declared := make(map[string]bool)
	var pendingToolCallIDs []string
	toolSeq := 0
	for _, m := range msgs {
		msgMap, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msgMap["role"].(string)
		if role != "assistant" {
			continue
		}
		if tcs, ok := msgMap["tool_calls"].([]any); ok && len(tcs) > 0 {
			for k, tcRaw := range tcs {
				if tcMap, ok := tcRaw.(map[string]any); ok {
					id, _ := tcMap["id"].(string)
					if id == "" {
						id = fmt.Sprintf("call_%d_%d", toolSeq, k)
						tcMap["id"] = id
					}
					declared[id] = true
					pendingToolCallIDs = append(pendingToolCallIDs, id)
				}
			}
			toolSeq++
		}
	}
	// Pass 2: pair every tool result, or demote it to a user message.
	for _, m := range msgs {
		msgMap, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msgMap["role"].(string)
		if role != "tool" {
			continue
		}
		id, _ := msgMap["tool_call_id"].(string)
		if id != "" && declared[id] {
			for idx, p := range pendingToolCallIDs {
				if p == id {
					pendingToolCallIDs = append(pendingToolCallIDs[:idx], pendingToolCallIDs[idx+1:]...)
					break
				}
			}
			continue
		}
		if id == "" && len(pendingToolCallIDs) > 0 {
			msgMap["tool_call_id"] = pendingToolCallIDs[0]
			pendingToolCallIDs = pendingToolCallIDs[1:]
			continue
		}
		// Orphan: empty id with no declared call left to pair with, or an
		// id no assistant message in this body declares. Never fabricate
		// an id — demote the result to a user message instead.
		msgMap["role"] = "user"
		delete(msgMap, "tool_call_id")
	}
}

// repairToolCallIDsInJSON repairs missing tool_call_id directly on raw JSON body if it contains messages.
func repairToolCallIDsInJSON(body []byte) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	if _, ok := m["messages"]; ok {
		repairToolCallIDsInMap(m)
		if out, err := json.Marshal(m); err == nil {
			return out
		}
	}
	return body
}
