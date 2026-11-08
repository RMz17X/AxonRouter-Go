package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"axonrouter/proxy/internal/providers"
)

// TestForward_DahlMiniMaxThinkLeakPrevention verifies that when an upstream
// emits raw <think> tags in SSE delta.content, the forwarder extracts it into
// reasoning_content and strips it from content.
func TestForward_DahlMiniMaxThinkLeakPrevention(t *testing.T) {
	upstreamSSE := "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"<think>The user asks 1+1\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" which is 2.</think>\\n\\n2.\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(upstreamSSE))
	}))
	defer ts.Close()

	h := &ChatHandler{Client: ts.Client()}
	cfg := &providers.ProviderConfig{
		BaseURL: ts.URL,
	}

	rec := httptest.NewRecorder()
	metrics := &streamMetrics{}

	err := h.forwardRequest(
		context.Background(),
		rec,
		cfg,
		"test-key",
		[]byte(`{"model":"MiniMaxAI/MiniMax-M2.7","messages":[{"role":"user","content":"1+1"}]}`),
		true,  // isStream
		false, // translateResponse (OpenAI client)
		metrics,
		ts.Client(),
		"dahl",
		false,
		false,
	)

	if err != nil {
		t.Fatalf("forwardRequest failed: %v", err)
	}

	respBody := rec.Body.String()

	if strings.Contains(respBody, "<think>") || strings.Contains(respBody, "</think>") {
		t.Fatalf("LEAK: <think> tags were relayed to client in SSE stream: %s", respBody)
	}

	if !strings.Contains(respBody, `"reasoning_content":"The user asks 1+1"`) || !strings.Contains(respBody, `"reasoning_content":" which is 2."`) {
		t.Errorf("expected streaming reasoning_content chunks in SSE stream, got: %s", respBody)
	}

	if !strings.Contains(respBody, `"content":"2."`) {
		t.Errorf("expected clean content '2.' in SSE stream, got: %s", respBody)
	}
}

// TestForward_DahlNonStreamThinkLeakPrevention verifies non-streaming responses.
func TestForward_DahlNonStreamThinkLeakPrevention(t *testing.T) {
	upstreamJSON := `{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","content":"<think>deep reasoning</think>\n\nFinal answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":10}}`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(upstreamJSON))
	}))
	defer ts.Close()

	h := &ChatHandler{Client: ts.Client()}
	cfg := &providers.ProviderConfig{
		BaseURL: ts.URL,
	}

	rec := httptest.NewRecorder()
	metrics := &streamMetrics{}

	err := h.forwardRequest(
		context.Background(),
		rec,
		cfg,
		"test-key",
		[]byte(`{"model":"MiniMaxAI/MiniMax-M2.7","messages":[{"role":"user","content":"1+1"}]}`),
		false, // isStream = false
		false, // translateResponse = false
		metrics,
		ts.Client(),
		"dahl",
		false,
		false,
	)

	if err != nil {
		t.Fatalf("forwardRequest failed: %v", err)
	}

	respBody := rec.Body.String()

	if strings.Contains(respBody, "<think>") || strings.Contains(respBody, "</think>") {
		t.Fatalf("LEAK: <think> tags relayed in non-streaming JSON: %s", respBody)
	}

	if !strings.Contains(respBody, `"reasoning_content":"deep reasoning"`) {
		t.Errorf("expected reasoning_content 'deep reasoning', got: %s", respBody)
	}

	if !strings.Contains(respBody, `"content":"Final answer"`) {
		t.Errorf("expected clean content 'Final answer', got: %s", respBody)
	}
}

// TestForward_DahlDeepSeekReasoningAliasLift verifies that vendor "reasoning"
// alias is lifted to "reasoning_content".
func TestForward_DahlDeepSeekReasoningAliasLift(t *testing.T) {
	upstreamSSE := "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reason\":\"dahl thought step 1\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"dahl thought step 2\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Final result\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(upstreamSSE))
	}))
	defer ts.Close()

	h := &ChatHandler{Client: ts.Client()}
	cfg := &providers.ProviderConfig{
		BaseURL: ts.URL,
	}

	rec := httptest.NewRecorder()
	metrics := &streamMetrics{}

	err := h.forwardRequest(
		context.Background(),
		rec,
		cfg,
		"test-key",
		[]byte(`{"model":"deepseek-ai/DeepSeek-V4-Flash-0731","messages":[{"role":"user","content":"hi"}]}`),
		true,  // isStream
		false, // translateResponse
		metrics,
		ts.Client(),
		"dahl",
		false,
		false,
	)

	if err != nil {
		t.Fatalf("forwardRequest failed: %v", err)
	}

	respBody := rec.Body.String()

	if !strings.Contains(respBody, `"reasoning_content":"dahl thought step 2"`) {
		t.Errorf("expected reasoning_content in SSE stream for reasoning alias, got: %s", respBody)
	}
}
