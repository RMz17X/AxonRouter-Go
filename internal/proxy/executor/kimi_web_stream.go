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

// The completion stream in Kimi Web's real SSE shape, captured live
// from kimi.moonshot.cn on 2026-11-11 (the written spec's section 6
// guessed a different, fictional format): every data line names its
// kind in the "event" field. A turn opens with session furniture —
// "req" (echoes the prompt under "content"), "resp", "zone_set",
// "loading", and "rename", whose "text" is the auto-generated CHAT
// TITLE (the prompt's head, truncated) — then "cmpl" events carry the
// answer deltas in "text", and "done" ends the turn. Only "cmpl" is
// answer text: folding any other event's text into the reply leaks
// the chat title into the content, which is exactly the bug this
// classification exists to prevent.

// kimiWebStreamEvent is one decoded data: line.
type kimiWebStreamEvent struct {
	Text  string `json:"text"`
	Event string `json:"event"`
	View  string `json:"view"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// kimiWebChunk is one classified piece of the stream.
type kimiWebChunk struct {
	text     string
	thinking string
	finish   string
	err      string
}

// kimiWebClassify folds one decoded event into a chunk. The second
// result is false for events that carry nothing for the client.
func kimiWebClassify(ev kimiWebStreamEvent) (kimiWebChunk, bool) {
	if ev.Error != nil && ev.Error.Message != "" {
		return kimiWebChunk{err: ev.Error.Message}, true
	}
	switch ev.Event {
	case "cmpl":
		if ev.Text == "" {
			return kimiWebChunk{}, false
		}
		// Thinking zones mark their deltas with a think view; the
		// mapping is defensive — no thinking model has been probed
		// live yet, and a wrong guess here only mislabels the delta
		// channel, never leaks furniture into the content.
		if ev.View == "think" || ev.View == "thinking" {
			return kimiWebChunk{thinking: ev.Text}, true
		}
		return kimiWebChunk{text: ev.Text}, true
	case "done":
		return kimiWebChunk{finish: "stop"}, true
	case "error":
		msg := ev.Text
		if msg == "" {
			msg = "upstream error event"
		}
		return kimiWebChunk{err: msg}, true
	}
	// Everything else (req, resp, ping, zone_set, loading, rename,
	// and anything unlisted) is session furniture: it carries no
	// answer text for the client, whatever its other fields hold.
	return kimiWebChunk{}, false
}

// kimiWebScanSSE reads the completion stream and hands each
// classified chunk to emit, in order. An upstream error event is
// delivered as a chunk and ends the scan without an error return.
func kimiWebScanSSE(r io.Reader, emit func(kimiWebChunk)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev kimiWebStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		chunk, useful := kimiWebClassify(ev)
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

// kimiWebStreamToClient relays the completion stream as OpenAI
// chunks: a role chunk, content/reasoning deltas, a finish chunk, and
// the [DONE] sentinel.
func kimiWebStreamToClient(w http.ResponseWriter, req *Request, upstream io.Reader, model, responseID string) error {
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
	scanErr := kimiWebScanSSE(upstream, func(chunk kimiWebChunk) {
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
		_, _ = hw.Write(proxy.BuildStreamErrorBytes(http.StatusBadGateway, "kimi-web: "+scanErr.Error(), proxy.SSEFormatOpenAI))
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

// kimiWebCollectToClient drains the completion stream into one
// chat.completion. An upstream error event fails the turn, so the
// router can fail over to another account instead of serving an empty
// success.
func kimiWebCollectToClient(w http.ResponseWriter, req *Request, upstream io.Reader, model, responseID string, messages []map[string]any) error {
	var text, thinking strings.Builder
	var streamErr string
	finish := "stop"

	err := kimiWebScanSSE(upstream, func(chunk kimiWebChunk) {
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
		return proxy.UpstreamFailure(http.StatusBadGateway, "kimi-web: "+err.Error())
	}
	if streamErr != "" {
		return proxy.UpstreamFailure(http.StatusBadGateway, "kimi-web: "+streamErr)
	}

	message := map[string]any{"role": "assistant", "content": text.String()}
	if thinking.Len() > 0 {
		message["reasoning_content"] = thinking.String()
	}
	promptChars := 0
	for _, m := range messages {
		if content, _ := m["content"].(string); content != "" {
			promptChars += len(content)
		}
	}
	promptTokens := (promptChars + 3) / 4
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
		return proxy.UpstreamFailure(http.StatusInternalServerError, "kimi-web: marshal response: "+err.Error())
	}
	return jsonResponse(req.Ctx, w, bytes.NewReader(encoded), req.TranslateResp, req.ResponseBuf)
}
