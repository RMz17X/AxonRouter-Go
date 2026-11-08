package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	json "encoding/json/v2"

	"axonrouter/proxy/internal/constants"
	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/proxy"
)

const (
	perchDefaultOrigin   = "https://app.perchai.app"
	perchTurnTicketPath  = "/api/perch-terminal/turn-ticket"
	perchModelCallPath   = "/api/perch-terminal/model-call"
	perchCLIUserAgent    = "perchai-cli/2.4.125"
	perchSystemMarker    = "You are Saffron — Perch's workspace operator.\nSharp, experienced, been around long enough to know what matters and what's noise.\nYou work with analysts, operators, and founders on financial analysis, audits,\ndocument review, and whatever the workspace needs."
	perchTurnTicketTTL   = 4 * time.Minute
	perchDefaultMaxLimit = 10 << 20 // 10MB
)

type perchTicketEntry struct {
	ticket    string
	ticketID  string
	runID     string
	expiresAt time.Time
}

var (
	perchTicketMu    sync.RWMutex
	perchTicketCache = make(map[string]*perchTicketEntry) // key: sha256(token)
)

func perchTokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func getOrAcquirePerchTicket(ctx context.Context, client *http.Client, origin, token string) (*perchTicketEntry, error) {
	if token == "" {
		return nil, fmt.Errorf("perch: access token is required")
	}
	key := perchTokenKey(token)

	perchTicketMu.RLock()
	if entry, ok := perchTicketCache[key]; ok && time.Now().Add(30*time.Second).Before(entry.expiresAt) {
		perchTicketMu.RUnlock()
		return entry, nil
	}
	perchTicketMu.RUnlock()

	perchTicketMu.Lock()
	defer perchTicketMu.Unlock()

	// Double-check under write lock
	if entry, ok := perchTicketCache[key]; ok && time.Now().Add(30*time.Second).Before(entry.expiresAt) {
		return entry, nil
	}

	turnTicketURL := strings.TrimRight(origin, "/") + perchTurnTicketPath
	bodyBytes := []byte(`{"surface":"cli","profile":"standard"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, turnTicketURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("perch: new ticket request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", perchCLIUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perch: ticket request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("perch: read ticket response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: respBody}
	}

	var parsed struct {
		OK        bool   `json:"ok"`
		Ticket    string `json:"ticket"`
		TicketID  string `json:"ticketId"`
		RunID     string `json:"runId"`
		ExpiresAt any    `json:"expiresAt"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("perch: unmarshal ticket response: %w", err)
	}
	if !parsed.OK || parsed.Ticket == "" {
		msg := parsed.Error
		if msg == "" {
			msg = "failed to acquire turn ticket"
		}
		return nil, fmt.Errorf("perch: %s", msg)
	}

	exp := time.Now().Add(perchTurnTicketTTL)
	if parsed.ExpiresAt != nil {
		switch v := parsed.ExpiresAt.(type) {
		case float64:
			if v > 1e11 {
				exp = time.UnixMilli(int64(v))
			} else if v > 0 {
				exp = time.Unix(int64(v), 0)
			}
		case string:
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				exp = t
			}
		}
	}

	entry := &perchTicketEntry{
		ticket:    parsed.Ticket,
		ticketID:  parsed.TicketID,
		runID:     parsed.RunID,
		expiresAt: exp,
	}
	perchTicketCache[key] = entry
	return entry, nil
}

func clearPerchTicket(token string) {
	key := perchTokenKey(token)
	perchTicketMu.Lock()
	delete(perchTicketCache, key)
	perchTicketMu.Unlock()
}

func perchExtractUserID(token string, connData map[string]any) string {
	if connData != nil {
		if uid, ok := connData["userId"].(string); ok && strings.TrimSpace(uid) != "" {
			return strings.TrimSpace(uid)
		}
		if uid, ok := connData["sub"].(string); ok && strings.TrimSpace(uid) != "" {
			return strings.TrimSpace(uid)
		}
	}
	parts := strings.Split(token, ".")
	if len(parts) >= 2 {
		payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			payloadBytes, _ = base64.URLEncoding.DecodeString(parts[1])
		}
		if len(payloadBytes) > 0 {
			var claims struct {
				Sub string `json:"sub"`
			}
			if err := json.Unmarshal(payloadBytes, &claims); err == nil && claims.Sub != "" {
				return claims.Sub
			}
		}
	}
	return "perch-cli-user"
}

func ensurePerchMarker(messages []any) []any {
	if len(messages) == 0 {
		return []any{map[string]any{"role": "system", "content": perchSystemMarker}}
	}
	first, ok := messages[0].(map[string]any)
	if !ok {
		return append([]any{map[string]any{"role": "system", "content": perchSystemMarker}}, messages...)
	}
	role, _ := first["role"].(string)
	if role == "system" {
		content, _ := first["content"].(string)
		if strings.Contains(content, "You are Saffron") {
			return messages
		}
		newMsgs := make([]any, len(messages))
		copy(newMsgs, messages)
		newFirst := make(map[string]any)
		for k, v := range first {
			newFirst[k] = v
		}
		if strings.TrimSpace(content) == "" {
			newFirst["content"] = perchSystemMarker
		} else {
			newFirst["content"] = perchSystemMarker + "\n\n" + content
		}
		newMsgs[0] = newFirst
		return newMsgs
	}
	return append([]any{map[string]any{"role": "system", "content": perchSystemMarker}}, messages...)
}

func buildPerchEnvelope(body []byte, model string, ticket *perchTicketEntry, userID string) ([]byte, error) {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("perch: unmarshal request body: %w", err)
	}

	if msgs, ok := parsed["messages"].([]any); ok {
		parsed["messages"] = ensurePerchMarker(msgs)
	} else {
		parsed["messages"] = []any{map[string]any{"role": "system", "content": perchSystemMarker}}
	}

	delete(parsed, "model")

	lane := "chat"
	var preferredModel any = model
	if model == "" || model == "combo" || model == "default" || model == "auto" || model == "standard" {
		preferredModel = nil
	}
	envelope := map[string]any{
		"request":          parsed,
		"runId":            ticket.runID,
		"lane":             lane,
		"clientSurface":    "cli",
		"strictManual":     false,
		"preferredModelId": preferredModel,
		"attribution": map[string]any{
			"userId": userID,
			"runId":  ticket.runID,
			"lane":   lane,
			"source": "cli",
		},
	}
	return json.Marshal(envelope)
}

type perchSSEEvent struct {
	Type      string      `json:"type"`
	Text      string      `json:"text"`
	Delta     string      `json:"delta"`
	Reason    string      `json:"reason"`
	OK        *bool       `json:"ok,omitempty"`
	Error     string      `json:"error,omitempty"`
	ErrorCode string      `json:"errorCode,omitempty"`
	Provider  string      `json:"provider,omitempty"`
	Model     string      `json:"model,omitempty"`
	ToolCalls []any       `json:"toolCalls,omitempty"`
	Usage     *perchUsage `json:"usage,omitempty"`
}

type perchUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// ForwardPerch serves Perch AI completions: turn-ticket gate, cli envelope,
// and SSE stream translation into standard OpenAI chat completions.
func ForwardPerch(w http.ResponseWriter, req *Request) error {
	if req == nil || req.Config == nil {
		return proxy.UpstreamFailure(http.StatusInternalServerError, "perch: executor misconfigured")
	}
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	client := req.Client
	if client == nil {
		client = http.DefaultClient
	}
	token := req.APIKey
	if token == "" {
		return &proxy.UpstreamError{
			StatusCode: http.StatusUnauthorized,
			Body:       []byte(`{"error":{"message":"perch: connection is missing the access token","type":"auth_error","code":401}}`),
		}
	}

	origin := req.Config.BaseURL
	if origin == "" {
		origin = perchDefaultOrigin
	}
	if strings.Contains(origin, "/api/") {
		origin = strings.Split(origin, "/api/")[0]
	}
	origin = strings.TrimRight(origin, "/")

	var parsedBody map[string]any
	if err := json.Unmarshal(req.Body, &parsedBody); err != nil {
		return proxy.UpstreamFailure(http.StatusBadRequest, fmt.Sprintf("perch: invalid JSON body: %v", err))
	}
	model := strings.TrimSpace(req.ModelName)
	if model == "" {
		if m, ok := parsedBody["model"].(string); ok {
			model = strings.TrimSpace(m)
		}
	}
	if model == "" {
		model = "deepseek-flash-starter"
	}
	// Strip provider prefixes (e.g. "perch/", "perch:")
	for _, pfx := range []string{"perch/", "perch:", "perchai/", "perchai:"} {
		if rest, ok := strings.CutPrefix(model, pfx); ok {
			model = strings.TrimSpace(rest)
			break
		}
	}

	userID := perchExtractUserID(token, req.ConnData)

	ticket, err := getOrAcquirePerchTicket(ctx, client, origin, token)
	if err != nil {
		log.Warn("perch", "acquire turn ticket failed", "error", err)
		return err
	}

	envelope, err := buildPerchEnvelope(req.Body, model, ticket, userID)
	if err != nil {
		return proxy.UpstreamFailure(http.StatusBadRequest, err.Error())
	}

	callURL := strings.TrimRight(origin, "/") + perchModelCallPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, callURL, bytes.NewReader(envelope))
	if err != nil {
		return proxy.UpstreamFailure(http.StatusInternalServerError, err.Error())
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("User-Agent", perchCLIUserAgent)
	httpReq.Header.Set("x-perch-turn-ticket", ticket.ticket)
	if req.IsStream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return err
	}

	// If 401 on model call, clear ticket and retry once
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		clearPerchTicket(token)
		ticket, err = getOrAcquirePerchTicket(ctx, client, origin, token)
		if err != nil {
			return err
		}
		envelope, err = buildPerchEnvelope(req.Body, model, ticket, userID)
		if err != nil {
			return proxy.UpstreamFailure(http.StatusBadRequest, err.Error())
		}
		httpReq, err = http.NewRequestWithContext(ctx, http.MethodPost, callURL, bytes.NewReader(envelope))
		if err != nil {
			return proxy.UpstreamFailure(http.StatusInternalServerError, err.Error())
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+token)
		httpReq.Header.Set("User-Agent", perchCLIUserAgent)
		httpReq.Header.Set("x-perch-turn-ticket", ticket.ticket)
		if req.IsStream {
			httpReq.Header.Set("Accept", "text/event-stream")
		}
		resp, err = client.Do(httpReq)
		if err != nil {
			return err
		}
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: respBody}
	}

	responseID := fmt.Sprintf("chatcmpl-perch-%d", time.Now().UnixNano())
	created := time.Now().Unix()

	if req.IsStream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, _ := w.(http.Flusher)

		emit := func(chunk map[string]any) error {
			if req.TTFT != nil && *req.TTFT == 0 {
				*req.TTFT = time.Since(req.StartTime).Milliseconds()
			}
			b, err := json.Marshal(chunk)
			if err != nil {
				return err
			}
			if _, err := w.Write([]byte("data: " + string(b) + "\n\n")); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
			return nil
		}

		baseChunk := func(choices []map[string]any) map[string]any {
			return map[string]any{
				"id":      responseID,
				"object":  "chat.completion.chunk",
				"created": created,
				"model":   model,
				"choices": choices,
			}
		}

		// Initial chunk with role
		_ = emit(baseChunk([]map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}}))

		scanner := bufio.NewScanner(resp.Body)
		buf := make([]byte, 64*1024)
		scanner.Buffer(buf, constants.MaxUpstreamBodyBytes)

		var sawFinish bool
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if len(payload) == 0 {
				continue
			}
			if string(payload) == "[DONE]" {
				break
			}

			var ev perchSSEEvent
			if err := json.Unmarshal(payload, &ev); err != nil {
				continue
			}

			switch ev.Type {
			case "answer_delta":
				txt := ev.Text
				if txt == "" {
					txt = ev.Delta
				}
				if txt != "" {
					_ = emit(baseChunk([]map[string]any{{"index": 0, "delta": map[string]any{"content": txt}, "finish_reason": nil}}))
				}
			case "reasoning_delta":
				txt := ev.Text
				if txt == "" {
					txt = ev.Delta
				}
				if txt != "" {
					_ = emit(baseChunk([]map[string]any{{"index": 0, "delta": map[string]any{"reasoning_content": txt}, "finish_reason": nil}}))
				}
			case "tool_call_delta", "tool_use_end":
				if len(ev.ToolCalls) > 0 {
					_ = emit(baseChunk([]map[string]any{{"index": 0, "delta": map[string]any{"tool_calls": ev.ToolCalls}, "finish_reason": nil}}))
				}
			case "done":
				sawFinish = true
				if ev.OK != nil && !*ev.OK {
					errMsg := ev.Error
					if errMsg == "" {
						errMsg = "perch model call failed"
					}
					chunk := baseChunk(nil)
					chunk["error"] = map[string]any{"message": errMsg, "type": "api_error"}
					_ = emit(chunk)
					_, _ = w.Write([]byte("data: [DONE]\n\n"))
					return nil
				}
				finReason := "stop"
				if len(ev.ToolCalls) > 0 {
					finReason = "tool_calls"
				}
				finChunk := baseChunk([]map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": finReason}})
				if ev.Usage != nil {
					finChunk["usage"] = map[string]any{
						"prompt_tokens":     ev.Usage.InputTokens,
						"completion_tokens": ev.Usage.OutputTokens,
						"total_tokens":      ev.Usage.InputTokens + ev.Usage.OutputTokens,
					}
				}
				_ = emit(finChunk)
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
				return nil
			}
		}

		if !sawFinish {
			_ = emit(baseChunk([]map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		}
		return nil
	}

	// Non-streaming: read events or JSON and accumulate output
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, constants.MaxUpstreamBodyBytes))
	if err != nil {
		return proxy.UpstreamFailure(http.StatusBadGateway, fmt.Sprintf("perch: read upstream response: %v", err))
	}

	var answerText strings.Builder
	var reasoningText strings.Builder
	var toolCalls []any
	var finalUsage *perchUsage
	var upstreamErr error

	// If upstream returned a single JSON object instead of raw SSE
	trimmed := bytes.TrimSpace(respBytes)
	if bytes.HasPrefix(trimmed, []byte("{")) && bytes.HasSuffix(trimmed, []byte("}")) {
		var singleObj struct {
			OK           *bool       `json:"ok,omitempty"`
			Text         string      `json:"text,omitempty"`
			FinishReason string      `json:"finishReason,omitempty"`
			Error        string      `json:"error,omitempty"`
			ToolCalls    []any       `json:"toolCalls,omitempty"`
			Usage        *perchUsage `json:"usage,omitempty"`
		}
		if err := json.Unmarshal(trimmed, &singleObj); err == nil {
			if singleObj.OK != nil && !*singleObj.OK {
				errMsg := singleObj.Error
				if errMsg == "" {
					errMsg = "perch model call failed"
				}
				return proxy.UpstreamFailure(http.StatusBadGateway, errMsg)
			}
			answerText.WriteString(singleObj.Text)
			if len(singleObj.ToolCalls) > 0 {
				toolCalls = singleObj.ToolCalls
			}
			if singleObj.Usage != nil {
				finalUsage = singleObj.Usage
			}
		}
	}

	if answerText.Len() == 0 {
		scanner := bufio.NewScanner(bytes.NewReader(respBytes))
		buf := make([]byte, 64*1024)
		scanner.Buffer(buf, constants.MaxUpstreamBodyBytes)

		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if !bytes.HasPrefix(line, []byte("data:")) {
				// Also handle raw JSON lines
				if bytes.HasPrefix(line, []byte("{")) {
					var ev perchSSEEvent
					if err := json.Unmarshal(line, &ev); err == nil {
						if ev.Text != "" {
							answerText.WriteString(ev.Text)
						} else if ev.Delta != "" {
							answerText.WriteString(ev.Delta)
						}
						if ev.Usage != nil {
							finalUsage = ev.Usage
						}
					}
				}
				continue
			}
			payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if len(payload) == 0 || string(payload) == "[DONE]" {
				continue
			}

			var ev perchSSEEvent
			if err := json.Unmarshal(payload, &ev); err != nil {
				continue
			}

			switch ev.Type {
			case "answer_delta":
				txt := ev.Text
				if txt == "" {
					txt = ev.Delta
				}
				answerText.WriteString(txt)
			case "reasoning_delta":
				txt := ev.Text
				if txt == "" {
					txt = ev.Delta
				}
				reasoningText.WriteString(txt)
			case "tool_call_delta", "tool_use_end":
				if len(ev.ToolCalls) > 0 {
					toolCalls = ev.ToolCalls
				}
			case "done":
				if ev.OK != nil && !*ev.OK {
					errMsg := ev.Error
					if errMsg == "" {
						errMsg = "perch model call failed"
					}
					upstreamErr = errors.New(errMsg)
				}
				if ev.Text != "" && answerText.Len() == 0 {
					answerText.WriteString(ev.Text)
				}
				if len(ev.ToolCalls) > 0 {
					toolCalls = ev.ToolCalls
				}
				if ev.Usage != nil {
					finalUsage = ev.Usage
				}
			}
		}
	}

	if upstreamErr != nil {
		return proxy.UpstreamFailure(http.StatusBadGateway, upstreamErr.Error())
	}

	msg := map[string]any{
		"role":    "assistant",
		"content": answerText.String(),
	}
	if reasoningText.Len() > 0 {
		msg["reasoning_content"] = reasoningText.String()
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}

	finReason := "stop"
	if len(toolCalls) > 0 {
		finReason = "tool_calls"
	}

	usageMap := map[string]any{
		"prompt_tokens":     0,
		"completion_tokens": 0,
		"total_tokens":      0,
	}
	if finalUsage != nil {
		usageMap["prompt_tokens"] = finalUsage.InputTokens
		usageMap["completion_tokens"] = finalUsage.OutputTokens
		usageMap["total_tokens"] = finalUsage.InputTokens + finalUsage.OutputTokens
	}

	out := map[string]any{
		"id":      responseID,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": finReason,
		}},
		"usage": usageMap,
	}

	w.Header().Set("Content-Type", "application/json")
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}
