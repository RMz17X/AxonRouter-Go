package dashboard

import (
	"bytes"
	"context"
	"encoding/binary"
	json "encoding/json/v2"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"axonrouter/proxy/internal/translator"
)

// pingResult is AxonRouter ping.js's verdict shape: {ok, latencyMs, error,
// status} plus the reasoning-only soft-pass note. Error is a pointer so a
// success serializes as "error": null exactly like the original.
type pingResult struct {
	OK        bool    `json:"ok"`
	LatencyMs int64   `json:"latencyMs"`
	Error     *string `json:"error"`
	Status    *int    `json:"status,omitempty"`
	Note      string  `json:"note,omitempty"`
}

var pingHTTPClient = &http.Client{Timeout: 15 * time.Second}

func pingFailure(latencyMs int64, status int, message string) pingResult {
	return pingResult{OK: false, LatencyMs: latencyMs, Error: &message, Status: &status}
}

// pingModelByKind is a port of AxonRouter's pingModelByKind
// (src/app/api/models/test/ping.js): one self-call through this gateway's
// own /v1 surface, dispatched by kind, with the probe pinned to a
// connection when one is given. AxonRouter posts to /api/v1/* on its own
// origin; the same handlers here live at /v1/*.
//
// A transport-level failure (timeout, refused) is returned as an error,
// mirroring ping.js letting the fetch rejection propagate to its caller.
//
// Deviation, recorded: the pin travels as the same x-connection-id /
// x-connection-pin headers AxonRouter sends, and this gateway's media
// handlers honour x-connection-id — but the /v1 chat entry resolves its
// connection from the model string, not from headers, so an llm ping is
// routed by the provider's strategy rather than hard-pinned. The verdict
// (does this model answer through this provider) is unaffected; which
// sibling account served it can differ.
func (h *DashboardHandler) pingModelByKind(ctx context.Context, origin, fullModel, kind, connectionID string) (pingResult, error) {
	start := time.Now()
	path := "/v1/chat/completions"
	var body io.Reader
	contentType := "application/json"

	switch kind {
	case "embedding":
		path = "/v1/embeddings"
		encoded, _ := json.Marshal(map[string]any{"model": fullModel, "input": "test"})
		body = bytes.NewReader(encoded)
	case "image":
		path = "/v1/images/generations"
		encoded, _ := json.Marshal(map[string]any{"model": fullModel, "prompt": "test"})
		body = bytes.NewReader(encoded)
	case "stt":
		path = "/v1/audio/transcriptions"
		buf := &bytes.Buffer{}
		mw := multipart.NewWriter(buf)
		if file, err := mw.CreateFormFile("file", "test.wav"); err == nil {
			_, _ = file.Write(pingSilentWAV())
		}
		_ = mw.WriteField("model", fullModel)
		_ = mw.Close()
		body = buf
		contentType = mw.FormDataContentType()
	default:
		encoded, _ := json.Marshal(map[string]any{
			"model":      fullModel,
			"max_tokens": 1024,
			"stream":     false,
			"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		})
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+path, body)
	if err != nil {
		return pingResult{}, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("x-axonrouter-test-request", "1")
	if connectionID != "" {
		req.Header.Set("x-connection-id", connectionID)
		req.Header.Set("x-connection-pin", "strict")
	}
	if apiKey := h.firstActiveAPIKey(); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := pingHTTPClient.Do(req)
	latencyMs := time.Since(start).Milliseconds()
	if err != nil {
		return pingResult{}, err
	}
	defer resp.Body.Close()
	rawText, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	status := resp.StatusCode

	parsedRaw := translator.UnwrapClineEnvelope(rawText)
	var parsed map[string]any
	if len(parsedRaw) > 0 {
		_ = json.Unmarshal(parsedRaw, &parsed)
	}

	if status < 200 || status > 299 {
		detail := pingErrorDetail(parsed, rawText, kind == "embedding")
		limit := 240
		if kind != "embedding" && kind != "image" && kind != "stt" {
			limit = 500
		}
		message := fmt.Sprintf("HTTP %d", status)
		if detail != "" {
			message += ": " + truncateRunes(detail, limit)
		}
		return pingFailure(latencyMs, status, message), nil
	}

	switch kind {
	case "embedding":
		data, _ := parsed["data"].([]any)
		if len(data) == 0 {
			return pingFailure(latencyMs, status, "Provider returned no embedding data"), nil
		}
		first, _ := data[0].(map[string]any)
		if _, ok := first["embedding"].([]any); !ok {
			return pingFailure(latencyMs, status, "Provider returned no embedding data"), nil
		}
	case "image":
		data, _ := parsed["data"].([]any)
		if len(data) == 0 {
			return pingFailure(latencyMs, status, "Provider returned no image data for this model"), nil
		}
	case "stt":
		text, _ := parsed["text"].(string)
		if strings.TrimSpace(text) == "" {
			return pingFailure(latencyMs, status, "Provider returned no transcription text for this model"), nil
		}
	default:
		return pingChatVerdict(parsed, latencyMs, status), nil
	}
	return pingResult{OK: true, LatencyMs: latencyMs, Error: nil, Status: &status}, nil
}

// pingChatVerdict applies ping.js's LLM checks in order: a provider-level
// status envelope, an error envelope under HTTP 200, the reasoning-only
// soft pass, then the choices requirement.
func pingChatVerdict(parsed map[string]any, latencyMs int64, status int) pingResult {
	if parsed == nil {
		return pingFailure(latencyMs, status, "Provider returned no completion choices for this model")
	}
	if rawStatus, ok := parsed["status"]; ok && rawStatus != nil {
		statusText := fmt.Sprint(rawStatus)
		if statusText != "200" && statusText != "0" {
			msg := dataStr(parsed, "msg")
			if msg == "" {
				msg = dataStr(parsed, "message")
			}
			if msg != "" {
				return pingFailure(latencyMs, status, fmt.Sprintf("Provider status %s: %s", statusText, truncateRunes(msg, 240)))
			}
		}
	}
	errValue := parsed["error"]
	if errValue == nil {
		if dataObj, ok := parsed["data"].(map[string]any); ok {
			errValue = dataObj["error"]
		}
	}
	if errValue != nil {
		message := ""
		switch e := errValue.(type) {
		case string:
			message = e
		case map[string]any:
			message = dataStr(e, "message")
		}
		if message == "" {
			message = "Provider returned an error"
		}
		return pingFailure(latencyMs, status, truncateRunes(message, 240))
	}
	choices, _ := parsed["choices"].([]any)
	if len(choices) == 0 {
		return pingFailure(latencyMs, status, "Provider returned no completion choices for this model")
	}
	first, _ := choices[0].(map[string]any)
	message, _ := first["message"].(map[string]any)
	contentEmpty := true
	if message != nil {
		switch content := message["content"].(type) {
		case string:
			contentEmpty = strings.TrimSpace(content) == ""
		case nil:
			contentEmpty = true
		default:
			contentEmpty = false
		}
	}
	hasReasoning := false
	if message != nil {
		for _, key := range []string{"reasoning", "reasoning_content", "thinking", "thinking_content"} {
			if v, ok := message[key].(string); ok && v != "" {
				hasReasoning = true
			}
		}
	}
	if first["finish_reason"] == "length" && contentEmpty && hasReasoning {
		return pingResult{OK: true, LatencyMs: latencyMs, Error: nil, Status: &status, Note: "reasoning-only response (length-limited)"}
	}
	return pingResult{OK: true, LatencyMs: latencyMs, Error: nil, Status: &status}
}

// pingErrorDetail picks the failure detail the way ping.js does:
// error.message, then (except for embeddings) msg/message, then a string
// error, then the raw body.
func pingErrorDetail(parsed map[string]any, rawText []byte, embedding bool) string {
	if parsed != nil {
		if errObj, ok := parsed["error"].(map[string]any); ok {
			if msg := dataStr(errObj, "message"); msg != "" {
				return msg
			}
		}
		if !embedding {
			if msg := dataStr(parsed, "msg"); msg != "" {
				return msg
			}
			if msg := dataStr(parsed, "message"); msg != "" {
				return msg
			}
		}
		if errStr, ok := parsed["error"].(string); ok && errStr != "" {
			return errStr
		}
	}
	return strings.TrimSpace(string(rawText))
}

// truncateRunes mirrors JS String.prototype.slice for the error-detail
// limits (ping.js counts UTF-16 code units; runes are the closest Go
// equivalent and identical for ASCII).
func truncateRunes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

// pingSilentWAV builds the same 250 ms silent 16 kHz mono WAV ping.js
// uploads for STT probes.
func pingSilentWAV() []byte {
	const (
		sampleRate    = 16000
		channels      = 1
		bitsPerSample = 16
		durationMs    = 250
	)
	sampleCount := max(1, sampleRate*durationMs/1000)
	dataSize := sampleCount * channels * bitsPerSample / 8
	buf := make([]byte, 44+dataSize)
	copy(buf[0:], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:], uint32(36+dataSize))
	copy(buf[8:], "WAVE")
	copy(buf[12:], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:], 16)
	binary.LittleEndian.PutUint16(buf[20:], 1)
	binary.LittleEndian.PutUint16(buf[22:], channels)
	binary.LittleEndian.PutUint32(buf[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buf[28:], uint32(sampleRate*channels*bitsPerSample/8))
	binary.LittleEndian.PutUint16(buf[32:], channels*bitsPerSample/8)
	binary.LittleEndian.PutUint16(buf[34:], bitsPerSample)
	copy(buf[36:], "data")
	binary.LittleEndian.PutUint32(buf[40:], uint32(dataSize))
	return buf
}
