package executor

import (
	"bufio"
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"axonrouter/proxy/internal/proxy"
)

// The completion stream, in the two shapes claude.ai speaks. The spec
// (section 6) documents the classic shape — `event: completion` blocks
// whose data carries the text delta in "completion" and the end of the
// turn in "stop_reason". The site's messages rendering also emits the
// Anthropic-style typed events (content_block_delta / message_delta).
// The scanner accepts both, so the provider survives the site picking
// either rendering for a session.

// claudeWebStreamEvent is one decoded data: line, carrying the fields
// of both stream shapes.
type claudeWebStreamEvent struct {
	Completion string  `json:"completion"`
	StopReason *string `json:"stop_reason"`
	Type       string  `json:"type"`
	Delta      *struct {
		Type       string  `json:"type"`
		Text       string  `json:"text"`
		Thinking   string  `json:"thinking"`
		StopReason *string `json:"stop_reason"`
	} `json:"delta"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// claudeWebChunk is one classified piece of the stream: visible text,
// thinking text, a finish reason, or an upstream error.
type claudeWebChunk struct {
	text     string
	thinking string
	finish   string
	err      string
}

func claudeWebFinishReason(upstream string) string {
	if upstream == "max_tokens" {
		return "length"
	}
	return "stop"
}

// claudeWebClassify folds one decoded event into a chunk. The second
// result is false for events that carry nothing for the client (ping,
// message scaffolding).
func claudeWebClassify(ev claudeWebStreamEvent) (claudeWebChunk, bool) {
	if ev.Error != nil && ev.Error.Message != "" {
		return claudeWebChunk{err: ev.Error.Message}, true
	}
	// Anthropic-style typed events.
	switch ev.Type {
	case "content_block_delta":
		if ev.Delta == nil {
			return claudeWebChunk{}, false
		}
		if ev.Delta.Text != "" {
			return claudeWebChunk{text: ev.Delta.Text}, true
		}
		if ev.Delta.Thinking != "" {
			return claudeWebChunk{thinking: ev.Delta.Thinking}, true
		}
		return claudeWebChunk{}, false
	case "message_delta":
		if ev.Delta != nil && ev.Delta.StopReason != nil {
			return claudeWebChunk{finish: claudeWebFinishReason(*ev.Delta.StopReason)}, true
		}
		return claudeWebChunk{}, false
	case "message_stop":
		return claudeWebChunk{finish: "stop"}, true
	case "ping":
		return claudeWebChunk{}, false
	}
	// Spec shape: the completion field is the delta, stop_reason ends it.
	if ev.Completion != "" {
		return claudeWebChunk{text: ev.Completion}, true
	}
	if ev.StopReason != nil {
		return claudeWebChunk{finish: claudeWebFinishReason(*ev.StopReason)}, true
	}
	return claudeWebChunk{}, false
}

// claudeWebScanSSE reads the completion stream and hands each
// classified chunk to emit, in order. An upstream error event is
// delivered as a chunk and ends the scan without an error return.
func claudeWebScanSSE(r io.Reader, emit func(claudeWebChunk)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue // event:, id:, blank separators — the data carries the type
		}
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev claudeWebStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		chunk, useful := claudeWebClassify(ev)
		if !useful {
			continue
		}
		emit(chunk)
		if chunk.err != "" || chunk.finish != "" {
			return nil
		}
	}
	return scanner.Err()
}

// claudeWebStreamToClient relays the completion stream as OpenAI
// chunks: a role chunk, content/reasoning deltas, a finish chunk, and
// the [DONE] sentinel.
func claudeWebStreamToClient(w http.ResponseWriter, req *Request, upstream io.Reader, model, responseID string) error {
	hw := proxy.NewHeartbeatWriter(req.Ctx, w, 0)
	defer func() { _ = hw.Close() }()
	flusher := proxy.WriteSSEHeaders(hw)

	created := time.Now().Unix()
	startTime := req.StartTime
	if startTime.IsZero() {
		startTime = time.Now()
	}

	var emitErr error
	emit := func(delta map[string]any, finish any) error {
		if emitErr != nil {
			return emitErr
		}
		if req.TTFT != nil && *req.TTFT == 0 {
			*req.TTFT = time.Since(startTime).Milliseconds()
		}
		chunk := map[string]any{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		encoded, err := json.Marshal(chunk)
		if err != nil {
			emitErr = err
			return err
		}
		if _, err := hw.Write(fmt.Appendf(nil, "data: %s\n\n", encoded)); err != nil {
			emitErr = err
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	_ = emit(map[string]any{"role": "assistant"}, nil)

	finish := "stop"
	scanErr := claudeWebScanSSE(upstream, func(chunk claudeWebChunk) {
		switch {
		case chunk.err != "":
			_ = emit(map[string]any{"content": "[Error: " + chunk.err + "]"}, nil)
		case chunk.finish != "":
			finish = chunk.finish
		case chunk.thinking != "":
			_ = emit(map[string]any{"reasoning_content": chunk.thinking}, nil)
		case chunk.text != "":
			_ = emit(map[string]any{"content": chunk.text}, nil)
		}
	})
	if scanErr != nil {
		_, _ = hw.Write(proxy.BuildStreamErrorBytes(http.StatusBadGateway, "claude-web: "+scanErr.Error(), proxy.SSEFormatOpenAI))
		_, _ = hw.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		return scanErr
	}
	if emitErr != nil {
		return emitErr
	}
	_ = emit(map[string]any{}, finish)
	_, err := hw.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
	return err
}

// claudeWebCollectToClient drains the completion stream into one
// chat.completion. An upstream error event fails the turn, so the
// router can fail over to another account instead of serving an empty
// success.
func claudeWebCollectToClient(w http.ResponseWriter, req *Request, upstream io.Reader, model, responseID, prompt string) error {
	var text, thinking strings.Builder
	var streamErr string
	finish := "stop"

	err := claudeWebScanSSE(upstream, func(chunk claudeWebChunk) {
		switch {
		case chunk.err != "":
			streamErr = chunk.err
		case chunk.finish != "":
			finish = chunk.finish
		case chunk.thinking != "":
			thinking.WriteString(chunk.thinking)
		case chunk.text != "":
			text.WriteString(chunk.text)
		}
	})
	if err != nil {
		return proxy.UpstreamFailure(http.StatusBadGateway, "claude-web: "+err.Error())
	}
	if streamErr != "" {
		return proxy.UpstreamFailure(http.StatusBadGateway, "claude-web: "+streamErr)
	}

	message := map[string]any{"role": "assistant", "content": text.String()}
	if thinking.Len() > 0 {
		message["reasoning_content"] = thinking.String()
	}
	promptTokens := (len(prompt) + 3) / 4
	completionTokens := (text.Len() + thinking.Len() + 3) / 4
	body := map[string]any{
		"id":      responseID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{"index": 0, "message": message, "finish_reason": finish}},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return proxy.UpstreamFailure(http.StatusInternalServerError, "claude-web: marshal response: "+err.Error())
	}
	return jsonResponse(req.Ctx, w, bytes.NewReader(encoded), req.TranslateResp, req.ResponseBuf)
}
