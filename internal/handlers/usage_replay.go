package handlers

import (
	"bytes"
	"database/sql"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
)

// Request replay — port of AxonRouter's
// POST /api/usage/request-details/[id]/replay (route.ts).
//
// The stored detail is the payload logUsage/logUsageFailure write into
// requestDetails.data: provider, model, request.messages and
// response.content. Two Axon behaviours adapt to what that payload holds:
// the original request was recorded as messages only (no tools, temperature
// or other body fields), so an executing replay re-sends the stored
// conversation rather than the byte-exact original body; and dispatch goes
// to this gateway's chat endpoint (/v1/chat/completions) instead of Axon's
// /v1/unified, which axonrouter-go does not have. The preview object keeps
// Axon's field set, with correlationId, endpoint, mode and trace null —
// the stored payload carries none of them.

// HandleRequestDetailReplay serves POST /api/usage/request-details/{id}/replay.
// Without { execute: true } it answers the replay payload only; with it, the
// stored conversation is dispatched again and the new response is compared
// against the recorded one.
func HandleRequestDetailReplay(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := url.PathUnescape(r.PathValue("id"))
		if err != nil || id == "" {
			handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Missing request detail id"})
			return
		}

		raw, err := repo.GetRequestDetailByID(id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				handlerutil.WriteJSON(w, http.StatusNotFound, map[string]any{"error": "Request detail not found"})
				return
			}
			writeReplayFailure(w, err)
			return
		}
		var detail map[string]any
		if err := json.Unmarshal([]byte(raw), &detail); err != nil {
			writeReplayFailure(w, err)
			return
		}

		var body struct {
			Execute bool   `json:"execute"`
			Model   string `json:"model"`
		}
		if rawBody, readErr := io.ReadAll(io.LimitReader(r.Body, 64*1024)); readErr == nil {
			// Axon reads the body leniently: an unreadable one means a
			// preview, not an error.
			_ = json.Unmarshal(rawBody, &body)
		}

		requestObj, _ := detail["request"].(map[string]any)
		if requestObj == nil {
			requestObj = map[string]any{}
		}
		detailID, _ := detail["id"].(string)
		if detailID == "" {
			detailID = id
		}
		replay := map[string]any{
			"id":            detailID,
			"correlationId": nil,
			"endpoint":      nil,
			"mode":          nil,
			"provider":      detail["provider"],
			"model":         detail["model"],
			"request":       requestObj,
			"trace":         nil,
		}
		if !body.Execute {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"replay": replay})
			return
		}

		model := strings.TrimSpace(body.Model)
		if model == "" {
			model, _ = detail["model"].(string)
		}
		dispatch := map[string]any{"stream": false}
		if model != "" {
			dispatch["model"] = model
		}
		if messages, ok := requestObj["messages"]; ok {
			dispatch["messages"] = messages
		}
		payload, _ := json.Marshal(dispatch)

		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
			handlerutil.RequestOrigin(r)+"/v1/chat/completions", bytes.NewReader(payload))
		if err != nil {
			writeReplayFailure(w, err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		// Axon's replay dispatches without a credential because its unified
		// endpoint accepts the internal call; /v1 here enforces dashboard
		// API keys, so the first active key rides along when one exists —
		// without it an executing replay could only ever report a 401.
		if key := firstActiveAPIKey(repo); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}

		client := &http.Client{Timeout: 120 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			writeReplayFailure(w, err)
			return
		}
		defer resp.Body.Close()

		var responsePayload any
		contentType := resp.Header.Get("Content-Type")
		if strings.Contains(contentType, "application/json") {
			rawResp, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
			if len(rawResp) > 0 {
				_ = json.Unmarshal(rawResp, &responsePayload)
			}
		} else {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8*1024*1024))
			responsePayload = map[string]any{"output_kind": nil}
		}

		var previousContent any
		if responseObj, ok := detail["response"].(map[string]any); ok {
			previousContent = responseObj["content"]
		}
		nextContent := replayResponseContent(responsePayload)

		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"replay": replay,
			"execution": map[string]any{
				"status":   resp.StatusCode,
				"ok":       resp.StatusCode >= 200 && resp.StatusCode < 300,
				"response": responsePayload,
			},
			"comparison": map[string]any{
				"previousContent": previousContent,
				"nextContent":     nextContent,
				"changed":         !jsonValuesEqual(previousContent, nextContent),
				"mode":            nil,
				"provider":        detail["provider"],
				"model":           model,
			},
		})
	}
}

// writeReplayFailure emits Axon's catch-all shape for a replay that could
// not be built or dispatched: { error: "Failed to build replay payload",
// message }.
func writeReplayFailure(w http.ResponseWriter, err error) {
	handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
		"error":   "Failed to build replay payload",
		"message": err.Error(),
	})
}

// replayResponseContent extracts the new response's content for the
// comparison: the chat completion's message text when the dispatch produced
// one (mirroring how Axon reads its unified output), otherwise the whole
// response payload, as Axon falls back to.
func replayResponseContent(payload any) any {
	record, ok := payload.(map[string]any)
	if !ok {
		return payload
	}
	choices, _ := record["choices"].([]any)
	if len(choices) == 0 {
		return payload
	}
	first, _ := choices[0].(map[string]any)
	message, _ := first["message"].(map[string]any)
	switch content := message["content"].(type) {
	case string:
		if content != "" {
			return content
		}
	case []any:
		var parts []string
		for _, part := range content {
			if partRecord, ok := part.(map[string]any); ok {
				if text, ok := partRecord["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		if joined := strings.Join(parts, " "); joined != "" {
			return joined
		}
	}
	return payload
}

// jsonValuesEqual compares two decoded JSON values by their canonical
// encodings, the Go equivalent of Axon's JSON.stringify comparison.
func jsonValuesEqual(a, b any) bool {
	aEncoded, aErr := json.Marshal(a)
	bEncoded, bErr := json.Marshal(b)
	if aErr != nil || bErr != nil {
		return false
	}
	return bytes.Equal(aEncoded, bEncoded)
}

// firstActiveAPIKey returns the first active dashboard API key, or "" when
// none exists. (The dashboard package keeps its own copy for the combo test;
// the two handlers sit in different packages by design.)
func firstActiveAPIKey(repo *db.Repo) string {
	keys, err := repo.GetApiKeys()
	if err != nil {
		return ""
	}
	for _, key := range keys {
		if key != nil && key.IsActive == 1 && key.Key != "" {
			return key.Key
		}
	}
	return ""
}
