package chat

// Batch 6 — the /v1beta (Gemini-shaped) protocol surface, ported from
// AxonRouter src/app/api/v1beta/**. Axon answers these routes at both
// /v1beta/** and /api/v1beta/**; the alias fold in RequestLogger brings
// both spellings here. Two lanes:
//   - GET /v1beta/models: the static catalogue in Gemini's list shape.
//   - POST /v1beta/models/{model}:generateContent|:streamGenerateContent:
//     Gemini TTS requests pass through natively to Google with pooled
//     gemini credentials; everything else is converted to the internal
//     OpenAI shape, run through the chat pipeline, and converted back —
//     the conversions mirror Axon's route helpers field-for-field,
//     including their deliberate lossiness (text parts only, a fixed
//     generationConfig subset).

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"axonrouter/proxy/internal/handlers/shared"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/providers"
)

type v1betaModelEntry struct {
	Name                       string   `json:"name"`
	DisplayName                string   `json:"displayName"`
	Description                string   `json:"description"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	InputTokenLimit            int      `json:"inputTokenLimit"`
	OutputTokenLimit           int      `json:"outputTokenLimit"`
}

// HandleV1BetaModels serves GET /v1beta/models: every catalogue model as
// models/{provider}/{id}; gemini models additionally get an unprefixed
// models/{id} entry with streaming listed, exactly like Axon.
func (h *ChatHandler) HandleV1BetaModels(w http.ResponseWriter, r *http.Request) {
	providerIDs := make([]string, 0, len(providers.ProviderModels))
	for id := range providers.ProviderModels {
		providerIDs = append(providerIDs, id)
	}
	sort.Strings(providerIDs)

	seen := make(map[string]bool)
	models := make([]v1betaModelEntry, 0, 256)
	add := func(e v1betaModelEntry) {
		if seen[e.Name] {
			return
		}
		seen[e.Name] = true
		models = append(models, e)
	}
	for _, providerID := range providerIDs {
		for _, modelID := range providers.ProviderModels[providerID] {
			add(v1betaModelEntry{
				Name:                       "models/" + providerID + "/" + modelID,
				DisplayName:                modelID,
				Description:                fmt.Sprintf("%s model: %s", providerID, modelID),
				SupportedGenerationMethods: []string{"generateContent"},
				InputTokenLimit:            128000,
				OutputTokenLimit:           8192,
			})
			if providerID == "gemini" {
				add(v1betaModelEntry{
					Name:                       "models/" + modelID,
					DisplayName:                modelID,
					Description:                fmt.Sprintf("Gemini model: %s", modelID),
					SupportedGenerationMethods: []string{"generateContent", "streamGenerateContent"},
					InputTokenLimit:            128000,
					OutputTokenLimit:           8192,
				})
			}
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"models": models})
}

// writeV1BetaError writes Axon's Gemini-lane error envelope
// {error:{message}} (plus a numeric code where Axon's catch adds one).
func writeV1BetaError(w http.ResponseWriter, status int, message string) {
	errObj := map[string]any{"message": message}
	if status == http.StatusInternalServerError {
		errObj["code"] = 500
	}
	handlerutil.WriteJSON(w, status, map[string]any{"error": errObj})
}

// HandleV1BetaModelAction serves POST /v1beta/models/* — the
// :generateContent / :streamGenerateContent actions, with an optional
// provider prefix segment ({provider}/{model}:{action}).
func (h *ChatHandler) HandleV1BetaModelAction(w http.ResponseWriter, r *http.Request) {
	rest := chi.URLParam(r, "*")
	segments := strings.Split(strings.Trim(rest, "/"), "/")
	var model, modelAction string
	switch {
	case len(segments) >= 2:
		modelAction = segments[1]
		model = segments[0] + "/" + geminiActionName(modelAction)
	case len(segments) == 1 && segments[0] != "":
		modelAction = segments[0]
		model = geminiActionName(modelAction)
	default:
		writeV1BetaError(w, http.StatusInternalServerError, "missing model in path")
		return
	}
	action := ":generateContent"
	if strings.Contains(modelAction, ":streamGenerateContent") {
		action = ":streamGenerateContent"
	}

	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		writeV1BetaError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var body map[string]any
	if err := json.Unmarshal(rawBody, &body); err != nil {
		// Axon's outer catch: a body that does not parse surfaces as a
		// 500 {error:{message, code:500}}, not a 400.
		writeV1BetaError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if isGeminiNativeTTSRequest(model, body) {
		h.serveV1BetaNativeTTS(w, r, model, action, rawBody)
		return
	}
	stream := action == ":streamGenerateContent"
	internal := convertGeminiToInternal(body, model, stream)
	internalJSON, err := json.Marshal(internal)
	if err != nil {
		writeV1BetaError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.dispatchV1BetaConverted(w, r, internalJSON, model, stream)
}

// geminiActionName strips the :action suffix from a model path segment.
func geminiActionName(segment string) string {
	if i := strings.Index(segment, ":"); i >= 0 {
		return segment[:i]
	}
	return segment
}

// geminiModelID reduces a v1beta model reference to the bare model id:
// "models/gemini-x" and "gemini/gemini-x" both become "gemini-x".
func geminiModelID(model string) string {
	id := strings.TrimPrefix(model, "models/")
	id = strings.TrimPrefix(id, "gemini/")
	return id
}

// isGeminiNativeTTSRequest mirrors Axon's predicate: requests for a
// non-gemini provider prefix are never native; native requires an AUDIO
// response modality or a known Gemini TTS model.
func isGeminiNativeTTSRequest(model string, body map[string]any) bool {
	if strings.Contains(model, "/") &&
		!strings.HasPrefix(model, "gemini/") && !strings.HasPrefix(model, "models/") {
		return false
	}
	modelID := geminiModelID(model)
	if gc, ok := body["generationConfig"].(map[string]any); ok {
		if modalities, ok := gc["responseModalities"].([]any); ok {
			for _, m := range modalities {
				if s, ok := m.(string); ok && strings.EqualFold(s, "AUDIO") {
					return true
				}
			}
		}
	}
	return providers.GetProviderModelKind("gemini", modelID) == "tts"
}

var geminiModelIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_.:-]+$`)

// serveV1BetaNativeTTS forwards the request verbatim to Google's
// generativelanguage endpoint with the pooled gemini credentials,
// rotating connections on retryable upstream failures. Deviation from
// Axon: rotation does not write cooldown/strike state (Axon calls
// markAccountUnavailable / clearAccountError on its connection rows).
func (h *ChatHandler) serveV1BetaNativeTTS(w http.ResponseWriter, r *http.Request, model, action string, rawBody []byte) {
	modelID := geminiModelID(model)
	if !geminiModelIDPattern.MatchString(modelID) {
		writeV1BetaError(w, http.StatusBadRequest, "Invalid model")
		return
	}
	if h.Repo == nil {
		writeV1BetaError(w, http.StatusServiceUnavailable, "No active credentials for provider: gemini")
		return
	}
	conns, err := h.Repo.GetProviderConnections("gemini", true)
	if err != nil || len(conns) == 0 {
		writeV1BetaError(w, http.StatusServiceUnavailable, "No active credentials for provider: gemini")
		return
	}

	// The original query string rides along minus the client's own key.
	query := r.URL.Query()
	query.Del("key")
	upstreamURL := "https://generativelanguage.googleapis.com/v1beta/models/" + modelID + action
	if encoded := query.Encode(); encoded != "" {
		upstreamURL += "?" + encoded
	}

	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}
	lastStatus := 0
	lastError := ""
	for _, conn := range conns {
		var connData shared.ConnectionData
		if conn != nil && conn.Data != "" {
			_ = json.Unmarshal([]byte(conn.Data), &connData)
		}
		var authHeader, authValue string
		switch {
		case connData.APIKey != "":
			authHeader, authValue = "X-Goog-Api-Key", connData.APIKey
		case connData.AccessToken != "":
			authHeader, authValue = "Authorization", "Bearer "+connData.AccessToken
		default:
			writeV1BetaError(w, http.StatusNotFound, "No Gemini API key configured")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(rawBody))
		if err != nil {
			cancel()
			writeV1BetaError(w, http.StatusInternalServerError, err.Error())
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(authHeader, authValue)

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			if r.Context().Err() != nil {
				writeV1BetaError(w, 499, "Client closed request")
				return
			}
			status := http.StatusBadGateway
			if ctx.Err() == context.DeadlineExceeded {
				status = http.StatusGatewayTimeout
			}
			writeV1BetaError(w, status, err.Error())
			return
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			copyGeminiNativeHeaders(w, resp)
			w.WriteHeader(resp.StatusCode)
			_, _ = io.Copy(w, resp.Body)
			resp.Body.Close()
			cancel()
			return
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		lastStatus = resp.StatusCode
		lastError = strings.TrimSpace(string(respBody))
		if !v1betaFallbackStatus(resp.StatusCode) {
			copyGeminiNativeHeaders(w, resp)
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(respBody)
			return
		}
	}
	if lastStatus == 0 {
		lastStatus = http.StatusServiceUnavailable
	}
	if lastError == "" {
		lastError = "No active credentials for provider: gemini"
	}
	writeV1BetaError(w, lastStatus, lastError)
}

// v1betaFallbackStatus mirrors markAccountUnavailable's no-fallback set:
// client errors are returned to the caller, everything else rotates.
func v1betaFallbackStatus(status int) bool {
	switch status {
	case 400, 404, 413, 422, 499:
		return false
	}
	return true
}

// copyGeminiNativeHeaders forwards upstream headers minus the framing
// ones, plus Axon's wildcard CORS header.
func copyGeminiNativeHeaders(w http.ResponseWriter, resp *http.Response) {
	for k, values := range resp.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Content-Encoding", "Content-Length", "Transfer-Encoding":
			continue
		}
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
}

// convertGeminiToInternal ports Axon's convertGeminiToInternal: system
// instruction and content text parts only, generationConfig subset.
func convertGeminiToInternal(body map[string]any, model string, stream bool) map[string]any {
	messages := make([]any, 0)
	if si, ok := body["systemInstruction"].(map[string]any); ok {
		if text := geminiPartsText(si["parts"]); text != "" {
			messages = append(messages, map[string]any{"role": "system", "content": text})
		}
	}
	if contents, ok := body["contents"].([]any); ok {
		for _, item := range contents {
			cm, ok := item.(map[string]any)
			if !ok {
				continue
			}
			role := "user"
			if cm["role"] == "model" {
				role = "assistant"
			}
			messages = append(messages, map[string]any{
				"role":    role,
				"content": geminiPartsText(cm["parts"]),
			})
		}
	}
	out := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   stream,
	}
	if gc, ok := body["generationConfig"].(map[string]any); ok {
		if v, ok := gc["maxOutputTokens"]; ok {
			out["max_tokens"] = v
		}
		if v, ok := gc["temperature"]; ok {
			out["temperature"] = v
		}
		if v, ok := gc["topP"]; ok {
			out["top_p"] = v
		}
	}
	return out
}

// geminiPartsText joins the text of a Gemini parts array with "\n"
// (system) — contents use the same join in Axon (parts.map(text).join("\n")
// for systemInstruction; contents join with "\n" per part list as well in
// the shipped converter's text extraction).
func geminiPartsText(parts any) string {
	arr, ok := parts.([]any)
	if !ok {
		return ""
	}
	texts := make([]string, 0, len(arr))
	for _, p := range arr {
		if pm, ok := p.(map[string]any); ok {
			if t, ok := pm["text"].(string); ok {
				texts = append(texts, t)
			}
		}
	}
	return strings.Join(texts, "\n")
}

// dispatchV1BetaConverted runs the converted body through the chat
// handler in-process and converts the answer back to Gemini shape.
func (h *ChatHandler) dispatchV1BetaConverted(w http.ResponseWriter, r *http.Request, internalJSON []byte, model string, stream bool) {
	r2 := r.Clone(r.Context())
	r2.Method = http.MethodPost
	r2.Body = io.NopCloser(bytes.NewReader(internalJSON))
	r2.ContentLength = int64(len(internalJSON))
	r2.URL.Path = "/chat/completions"
	r2.URL.RawPath = ""

	if stream {
		tw := &geminiSSEWriter{w: w, model: model, header: make(http.Header)}
		h.HandleChatCompletions(tw, r2)
		tw.finish()
		return
	}

	cw := &captureResponseWriter{header: make(http.Header), status: 200}
	h.HandleChatCompletions(cw, r2)
	status := cw.status
	respBody := cw.buf.Bytes()

	var parsed map[string]any
	parseErr := json.Unmarshal(respBody, &parsed)
	_, hasCandidates := parsed["candidates"]
	_, hasError := parsed["error"]
	choices, _ := parsed["choices"].([]any)
	if status < 200 || status >= 300 || parseErr != nil || hasCandidates || hasError || len(choices) == 0 {
		// Passthrough: errors, already-Gemini bodies, and anything the
		// converter has no mapping for go back unchanged.
		copyCapturedHeaders(w, cw.header)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(status)
		_, _ = w.Write(respBody)
		return
	}
	converted := convertOpenAIResponseToGemini(parsed, model)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	out, _ := json.Marshal(converted)
	_, _ = w.Write(out)
}

func copyCapturedHeaders(w http.ResponseWriter, captured http.Header) {
	for k, values := range captured {
		if http.CanonicalHeaderKey(k) == "Content-Length" {
			continue
		}
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
}

// captureResponseWriter buffers a full non-stream response.
type captureResponseWriter struct {
	header http.Header
	status int
	buf    bytes.Buffer
	wrote  bool
}

func (c *captureResponseWriter) Header() http.Header { return c.header }

func (c *captureResponseWriter) WriteHeader(status int) {
	if c.wrote {
		return
	}
	c.wrote = true
	c.status = status
}

func (c *captureResponseWriter) Write(p []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	return c.buf.Write(p)
}

func (c *captureResponseWriter) Flush() {}

// convertOpenAIResponseToGemini ports Axon's
// convertOpenAIResponseToGemini for a parsed chat completion body.
func convertOpenAIResponseToGemini(body map[string]any, model string) map[string]any {
	choices, _ := body["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)

	parts := make([]any, 0, 2)
	if rc, ok := message["reasoning_content"].(string); ok && rc != "" {
		parts = append(parts, map[string]any{"text": rc, "thought": true})
	}
	content, _ := message["content"].(string)
	parts = append(parts, map[string]any{"text": content})

	out := map[string]any{
		"candidates": []any{
			map[string]any{
				"content":      map[string]any{"role": "model", "parts": parts},
				"finishReason": geminiFinishReason(choice["finish_reason"]),
				"index":        0,
			},
		},
		"modelVersion": geminiStringOr(body["model"], model),
	}
	if usage, ok := body["usage"].(map[string]any); ok {
		out["usageMetadata"] = geminiUsageMetadata(usage)
	}
	return out
}

// geminiFinishReason maps OpenAI finish reasons to Gemini's enum.
func geminiFinishReason(v any) string {
	switch v {
	case "length":
		return "MAX_TOKENS"
	case "content_filter":
		return "SAFETY"
	default:
		// stop, tool_calls, and anything else map to STOP in Axon.
		return "STOP"
	}
}

// geminiUsageMetadata maps an OpenAI usage object to Gemini's
// usageMetadata shape.
func geminiUsageMetadata(usage map[string]any) map[string]any {
	meta := map[string]any{
		"promptTokenCount":     geminiNum(usage["prompt_tokens"]),
		"candidatesTokenCount": geminiNum(usage["completion_tokens"]),
		"totalTokenCount":      geminiNum(usage["total_tokens"]),
	}
	if details, ok := usage["completion_tokens_details"].(map[string]any); ok {
		if rt, ok := details["reasoning_tokens"]; ok {
			if n := geminiNum(rt); n != 0 {
				meta["thoughtsTokenCount"] = n
			}
		}
	}
	return meta
}

// geminiNum normalises a decoded JSON number to an int when integral.
func geminiNum(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

func geminiStringOr(v any, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}

// geminiSSEWriter transforms an OpenAI SSE stream into Gemini chunks as
// it flows, mirroring Axon's transformOpenAISSEToGeminiSSE. Response
// headers are deferred until the first converted chunk so an upstream
// error can still pass through untouched.
type geminiSSEWriter struct {
	w        http.ResponseWriter
	model    string
	header   http.Header
	status   int
	failed   bool
	decided  bool
	buf      []byte
	finished bool
}

func (g *geminiSSEWriter) Header() http.Header { return g.header }

func (g *geminiSSEWriter) WriteHeader(status int) {
	if g.decided {
		return
	}
	g.status = status
	if status < 200 || status >= 300 {
		g.failed = true
		g.decided = true
		copyCapturedHeaders(g.w, g.header)
		g.w.WriteHeader(status)
	}
}

func (g *geminiSSEWriter) Write(p []byte) (int, error) {
	if g.failed {
		_, err := g.w.Write(p)
		return len(p), err
	}
	g.buf = append(g.buf, p...)
	for {
		i := bytes.IndexByte(g.buf, '\n')
		if i < 0 {
			break
		}
		line := g.buf[:i]
		g.buf = g.buf[i+1:]
		g.processLine(string(line))
	}
	return len(p), nil
}

func (g *geminiSSEWriter) Flush() {
	if g.failed {
		if f, ok := g.w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func (g *geminiSSEWriter) processLine(line string) {
	line = strings.TrimRight(line, "\r")
	if !strings.HasPrefix(line, "data:") {
		return
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "" || payload == "[DONE]" {
		return
	}
	var chunk map[string]any
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return
	}
	converted, ok := convertOpenAIChunkToGemini(chunk, g.model)
	if !ok {
		return
	}
	g.emit(converted)
}

func (g *geminiSSEWriter) emit(chunk map[string]any) {
	if !g.decided {
		g.decided = true
		g.w.Header().Set("Content-Type", "text/event-stream")
		g.w.Header().Set("Cache-Control", "no-cache")
		g.w.Header().Set("Access-Control-Allow-Origin", "*")
		g.w.WriteHeader(http.StatusOK)
	}
	out, err := json.Marshal(chunk)
	if err != nil {
		return
	}
	_, _ = g.w.Write([]byte("data: "))
	_, _ = g.w.Write(out)
	_, _ = g.w.Write([]byte("\r\n\r\n"))
	if f, ok := g.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *geminiSSEWriter) finish() {
	if g.finished {
		return
	}
	g.finished = true
	if g.failed {
		return
	}
	if len(g.buf) > 0 {
		g.processLine(string(g.buf))
		g.buf = nil
	}
	if !g.decided {
		// The upstream produced no convertible chunks; still answer as
		// an (empty) Gemini stream rather than leaving the client
		// hanging on an unwritten header.
		g.decided = true
		g.w.Header().Set("Content-Type", "text/event-stream")
		g.w.Header().Set("Cache-Control", "no-cache")
		g.w.Header().Set("Access-Control-Allow-Origin", "*")
		g.w.WriteHeader(http.StatusOK)
	}
}

// convertOpenAIChunkToGemini converts one OpenAI stream chunk; ok=false
// means Axon would skip the chunk (no choices, no content, no finish).
func convertOpenAIChunkToGemini(chunk map[string]any, model string) (map[string]any, bool) {
	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return nil, false
	}
	choice, _ := choices[0].(map[string]any)
	delta, _ := choice["delta"].(map[string]any)

	parts := make([]any, 0, 2)
	if rc, ok := delta["reasoning_content"].(string); ok && rc != "" {
		parts = append(parts, map[string]any{"text": rc, "thought": true})
	}
	if content, ok := delta["content"].(string); ok && content != "" {
		parts = append(parts, map[string]any{"text": content})
	}
	finishReason, hasFinish := choice["finish_reason"].(string)
	if len(parts) == 0 && !hasFinish {
		return nil, false
	}
	if len(parts) == 0 {
		parts = append(parts, map[string]any{"text": ""})
	}
	candidate := map[string]any{
		"content": map[string]any{"role": "model", "parts": parts},
		"index":   0,
	}
	if hasFinish {
		candidate["finishReason"] = geminiFinishReason(finishReason)
	}
	out := map[string]any{"candidates": []any{candidate}}
	if hasFinish {
		if usage, ok := chunk["usage"].(map[string]any); ok {
			out["usageMetadata"] = geminiUsageMetadata(usage)
		}
		out["modelVersion"] = geminiStringOr(chunk["model"], model)
	}
	return out, true
}
