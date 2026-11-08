package executor

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	json "encoding/json/v2"

	"axonrouter/proxy/internal/providers"
)

func TestPerch_EnsureMarker(t *testing.T) {
	// 1. Empty messages
	msgs1 := ensurePerchMarker([]any{})
	if len(msgs1) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs1))
	}
	m1 := msgs1[0].(map[string]any)
	if m1["role"] != "system" || m1["content"] != perchSystemMarker {
		t.Errorf("unexpected content: %v", m1)
	}

	// 2. Existing leading system message
	msgs2 := ensurePerchMarker([]any{
		map[string]any{"role": "system", "content": "Custom system prompt"},
		map[string]any{"role": "user", "content": "Hello"},
	})
	m2 := msgs2[0].(map[string]any)
	expected2 := perchSystemMarker + "\n\nCustom system prompt"
	if m2["content"] != expected2 {
		t.Errorf("expected %q, got %q", expected2, m2["content"])
	}

	// 3. Already marked system prompt - should be idempotent
	alreadyMarked := "You are Saffron — Perch's workspace operator.\nCustom prompt"
	msgs3 := ensurePerchMarker([]any{
		map[string]any{"role": "system", "content": alreadyMarked},
	})
	m3 := msgs3[0].(map[string]any)
	if m3["content"] != alreadyMarked {
		t.Errorf("unexpected re-marker: %v", m3["content"])
	}

	// 4. Leading user message (no system message)
	msgs4 := ensurePerchMarker([]any{
		map[string]any{"role": "user", "content": "Explain Go"},
	})
	if len(msgs4) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs4))
	}
	if msgs4[0].(map[string]any)["role"] != "system" || msgs4[1].(map[string]any)["role"] != "user" {
		t.Errorf("unexpected message order: %v", msgs4)
	}
}

func TestPerch_ExtractUserID(t *testing.T) {
	// 1. From connData userId
	uid1 := perchExtractUserID("tok", map[string]any{"userId": "custom-user-123"})
	if uid1 != "custom-user-123" {
		t.Errorf("expected custom-user-123, got %q", uid1)
	}

	// 2. From JWT token sub claim
	jwtPayload := `{"sub":"user-uuid-999","email":"test@example.com"}`
	encodedPayload := base64.RawURLEncoding.EncodeToString([]byte(jwtPayload))
	jwtToken := "header." + encodedPayload + ".sig"
	uid2 := perchExtractUserID(jwtToken, nil)
	if uid2 != "user-uuid-999" {
		t.Errorf("expected user-uuid-999, got %q", uid2)
	}

	// 3. Fallback
	uid3 := perchExtractUserID("opaque-token", nil)
	if uid3 != "perch-cli-user" {
		t.Errorf("expected perch-cli-user, got %q", uid3)
	}
}

func TestPerch_BuildEnvelope(t *testing.T) {
	body := []byte(`{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"hi"}]}`)
	ticket := &perchTicketEntry{
		ticket:   "tk_123",
		ticketID: "tid_123",
		runID:    "run_123",
	}
	envBytes, err := buildPerchEnvelope(body, "gpt-6.1-sol", ticket, "usr_123")
	if err != nil {
		t.Fatalf("build envelope: %v", err)
	}

	var env map[string]any
	if err := json.Unmarshal(envBytes, &env); err != nil {
		t.Fatalf("unmarshal env: %v", err)
	}

	if env["runId"] != "run_123" {
		t.Errorf("expected runId run_123, got %v", env["runId"])
	}
	if env["lane"] != "chat" {
		t.Errorf("expected lane chat, got %v", env["lane"])
	}
	if env["clientSurface"] != "cli" {
		t.Errorf("expected clientSurface cli, got %v", env["clientSurface"])
	}
	if env["preferredModelId"] != "gpt-6.1-sol" {
		t.Errorf("expected preferredModelId gpt-6.1-sol, got %v", env["preferredModelId"])
	}

	reqMap, ok := env["request"].(map[string]any)
	if !ok {
		t.Fatalf("expected request map, got %T", env["request"])
	}
	if _, ok := reqMap["model"]; ok {
		t.Errorf("expected model key deleted from nested request")
	}

	attr, ok := env["attribution"].(map[string]any)
	if !ok || attr["userId"] != "usr_123" || attr["source"] != "cli" {
		t.Errorf("invalid attribution: %v", attr)
	}
}

func TestPerch_GetOrAcquireTicket_And_Cache(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != perchTurnTicketPath {
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer test-tok") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		futureMs := time.Now().Add(10 * time.Minute).UnixMilli()
		_, _ = w.Write([]byte(fmt.Sprintf(`{
			"ok": true,
			"ticket": "ticket_token_abc",
			"ticketId": "ticket_id_123",
			"runId": "run_id_456",
			"expiresAt": %d,
			"enforced": true
		}`, futureMs)))
	}))
	defer srv.Close()

	token := fmt.Sprintf("test-tok-unique-%d", time.Now().UnixNano())
	// Test first fetch
	entry, err := getOrAcquirePerchTicket(context.Background(), srv.Client(), srv.URL, token)
	if err != nil {
		t.Fatalf("acquire ticket: %v", err)
	}
	if entry.ticket != "ticket_token_abc" || entry.runID != "run_id_456" {
		t.Errorf("unexpected ticket entry: %+v", entry)
	}

	// Test cached fetch (should not increment callCount)
	entry2, err := getOrAcquirePerchTicket(context.Background(), srv.Client(), srv.URL, token)
	if err != nil {
		t.Fatalf("cached acquire ticket: %v", err)
	}
	if entry2.ticket != entry.ticket {
		t.Errorf("expected cached ticket %q, got %q", entry.ticket, entry2.ticket)
	}
	if count := atomic.LoadInt32(&callCount); count != 1 {
		t.Errorf("expected 1 server call, got %d", count)
	}
}

func TestPerch_ForwardStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case perchTurnTicketPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok": true, "ticket": "tick1", "ticketId": "tid1", "runId": "rid1"}`))
		case perchModelCallPath:
			if r.Header.Get("x-perch-turn-ticket") != "tick1" {
				http.Error(w, "missing ticket", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			events := []string{
				`data: {"type": "reasoning_delta", "text": "thinking step 1"}`,
				`data: {"type": "answer_delta", "text": "Hello world"}`,
				`data: {"type": "done", "ok": true, "usage": {"inputTokens": 10, "outputTokens": 5}}`,
			}
			for _, ev := range events {
				_, _ = w.Write([]byte(ev + "\n\n"))
				if flusher != nil {
					flusher.Flush()
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:       context.Background(),
		Client:    srv.Client(),
		APIKey:    "test-stream-tok",
		ModelName: "perch/gemini-4-argon",
		IsStream:  true,
		Body:      []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
		Config: &providers.ProviderConfig{
			BaseURL: srv.URL,
		},
		StartTime: time.Now(),
	}

	err := ForwardPerch(rec, req)
	if err != nil {
		t.Fatalf("ForwardPerch: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "thinking step 1") {
		t.Errorf("stream missing reasoning delta: %s", body)
	}
	if !strings.Contains(body, "Hello world") {
		t.Errorf("stream missing answer delta: %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("stream missing [DONE]: %s", body)
	}
	if !strings.Contains(body, `"prompt_tokens":10`) {
		t.Errorf("stream missing prompt_tokens usage: %s", body)
	}
}

func TestPerch_ForwardNonStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case perchTurnTicketPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok": true, "ticket": "tick2", "ticketId": "tid2", "runId": "rid2"}`))
		case perchModelCallPath:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\": \"answer_delta\", \"text\": \"Full answer response\"}\n\n" +
				"data: {\"type\": \"done\", \"ok\": true, \"usage\": {\"inputTokens\": 15, \"outputTokens\": 7}}\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:       context.Background(),
		Client:    srv.Client(),
		APIKey:    "test-nonstream-tok",
		ModelName: "claude-sonnet-5.5",
		IsStream:  false,
		Body:      []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
		Config: &providers.ProviderConfig{
			BaseURL: srv.URL,
		},
		StartTime: time.Now(),
	}

	err := ForwardPerch(rec, req)
	if err != nil {
		t.Fatalf("ForwardPerch: %v", err)
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, rec.Body.String())
	}

	choices := res["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(choices))
	}
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "Full answer response" {
		t.Errorf("unexpected content: %v", msg["content"])
	}
	usage := res["usage"].(map[string]any)
	if usage["total_tokens"] != float64(22) {
		t.Errorf("expected 22 total tokens, got %v", usage["total_tokens"])
	}
}
