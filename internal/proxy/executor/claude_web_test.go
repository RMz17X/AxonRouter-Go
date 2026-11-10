package executor

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClaudeSite is a stand-in claude.ai that records what the executor
// sent and answers the spec's SSE completion shape. It proves the wire
// contract — endpoints, cookie, payload — without a real session.
type fakeClaudeSite struct {
	t *testing.T

	orgHits       atomic.Int32
	createHits    atomic.Int32
	completionHit atomic.Int32
	deleteHits    atomic.Int32

	lastCookie     string
	lastCreateBody map[string]any
	lastCompletion map[string]any
	anthropicStyle bool
}

func (f *fakeClaudeSite) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/organizations", func(w http.ResponseWriter, r *http.Request) {
		f.orgHits.Add(1)
		f.lastCookie = r.Header.Get("Cookie")
		_, _ = w.Write([]byte(`[{"uuid":"org-123","name":"Personal"}]`))
	})
	mux.HandleFunc("POST /api/organizations/org-123/chat_conversations", func(w http.ResponseWriter, r *http.Request) {
		f.createHits.Add(1)
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &f.lastCreateBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(body) // echo carries the uuid we generated
	})
	mux.HandleFunc("POST /api/organizations/org-123/chat_conversations/{id}/completion", func(w http.ResponseWriter, r *http.Request) {
		f.completionHit.Add(1)
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &f.lastCompletion)
		w.Header().Set("Content-Type", "text/event-stream")
		if f.anthropicStyle {
			_, _ = w.Write([]byte(
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"pikir \"}}\n\n" +
					"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Halo\"}}\n\n" +
					"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\" dunia\"}}\n\n" +
					"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"))
			return
		}
		_, _ = w.Write([]byte(
			"event: completion\ndata: {\"completion\":\"Halo\",\"stop_reason\":null}\n\n" +
				"event: completion\ndata: {\"completion\":\" dunia\",\"stop_reason\":null}\n\n" +
				"event: ping\ndata: {\"type\":\"ping\"}\n\n" +
				"event: completion\ndata: {\"completion\":\"\",\"stop_reason\":\"stop_sequence\"}\n\n"))
	})
	mux.HandleFunc("DELETE /api/organizations/org-123/chat_conversations/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.deleteHits.Add(1)
		_, _ = w.Write([]byte(`{}`))
	})
	return mux
}

func useFakeClaudeSite(t *testing.T, site *fakeClaudeSite) {
	t.Helper()
	server := httptest.NewServer(site.handler())
	t.Cleanup(server.Close)

	oldBase, oldFactory := claudeWebBaseURL, claudeWebTransportFactory
	claudeWebBaseURL = server.URL
	claudeWebTransportFactory = func(req *Request) (claudeWebDoer, error) {
		return &claudeWebPlainDoer{}, nil
	}
	t.Cleanup(func() { claudeWebBaseURL, claudeWebTransportFactory = oldBase, oldFactory })
}

func claudeWebTestRequest(body string, stream bool) *Request {
	return &Request{
		Body:     []byte(body),
		APIKey:   "sk-ant-sid01-testkey",
		IsStream: stream,
	}
}

func TestForwardClaudeWeb_StreamSpecShape(t *testing.T) {
	site := &fakeClaudeSite{t: t}
	useFakeClaudeSite(t, site)

	rec := httptest.NewRecorder()
	req := claudeWebTestRequest(
		`{"model":"claude-3-5-sonnet","messages":[{"role":"system","content":"Be brief."},{"role":"user","content":"Say hi"}]}`, true)
	if err := ForwardClaudeWeb(rec, req); err != nil {
		t.Fatalf("ForwardClaudeWeb: %v", err)
	}

	out := rec.Body.String()
	for _, want := range []string{`"content":"Halo"`, `"content":" dunia"`, `"finish_reason":"stop"`, "data: [DONE]"} {
		if !strings.Contains(out, want) {
			t.Errorf("stream output missing %q:\n%s", want, out)
		}
	}
	if site.orgHits.Load() != 1 || site.createHits.Load() != 1 || site.completionHit.Load() != 1 {
		t.Errorf("hits org/create/completion = %d/%d/%d, want 1/1/1",
			site.orgHits.Load(), site.createHits.Load(), site.completionHit.Load())
	}
	if site.lastCookie != "sessionKey=sk-ant-sid01-testkey" {
		t.Errorf("cookie header = %q", site.lastCookie)
	}
	if got := site.lastCompletion["model"]; got != "claude-3-5-sonnet-20241022" {
		t.Errorf("completion model = %v", got)
	}
	if got := site.lastCompletion["rendering_mode"]; got != "messages" {
		t.Errorf("rendering_mode = %v", got)
	}
	if got := site.lastCompletion["parent_message_uuid"]; got != claudeWebNullUUID {
		t.Errorf("parent_message_uuid = %v", got)
	}
	if _, present := site.lastCompletion["thinking"]; present {
		t.Errorf("thinking block sent for a non-thinking model")
	}
	prompt, _ := site.lastCompletion["prompt"].(string)
	if !strings.Contains(prompt, "system: Be brief.") || !strings.HasSuffix(prompt, "Say hi") {
		t.Errorf("flattened prompt = %q", prompt)
	}
	// The throwaway conversation is deleted off the client's path.
	deadline := time.Now().Add(2 * time.Second)
	for site.deleteHits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if site.deleteHits.Load() != 1 {
		t.Errorf("delete hits = %d, want 1", site.deleteHits.Load())
	}
}

func TestForwardClaudeWeb_CollectThinkingAnthropicShape(t *testing.T) {
	site := &fakeClaudeSite{t: t, anthropicStyle: true}
	useFakeClaudeSite(t, site)

	rec := httptest.NewRecorder()
	req := claudeWebTestRequest(
		`{"model":"claude-3-7-thinking","messages":[{"role":"user","content":"Think, then answer"}]}`, false)
	req.ConnData = map[string]any{"organizationUuid": "org-123", "userAgent": "Mozilla/5.0 (Windows NT 10.0) Chrome/133.0.0.0"}
	if err := ForwardClaudeWeb(rec, req); err != nil {
		t.Fatalf("ForwardClaudeWeb: %v", err)
	}
	if site.orgHits.Load() != 0 {
		t.Errorf("org discovery ran despite a stored organizationUuid")
	}
	thinking, _ := site.lastCompletion["thinking"].(map[string]any)
	if thinking == nil || thinking["budget_tokens"] != float64(4096) {
		t.Errorf("thinking block = %v", site.lastCompletion["thinking"])
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not a chat.completion: %v (%s)", err, rec.Body.String())
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "Halo dunia" {
		t.Errorf("collected content = %+v", resp.Choices)
	}
	if resp.Choices[0].Message.ReasoningContent != "pikir " {
		t.Errorf("reasoning_content = %q", resp.Choices[0].Message.ReasoningContent)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q", resp.Choices[0].FinishReason)
	}
}

func TestClaudeWebResolveCred(t *testing.T) {
	tests := []struct {
		name    string
		apiKey  string
		wantHdr string
		wantErr bool
	}{
		{"bare key", "sk-ant-sid01-abc", "sessionKey=sk-ant-sid01-abc", false},
		{"pair", "sessionKey=sk-ant-sid01-abc", "sessionKey=sk-ant-sid01-abc", false},
		{"full header", "sessionKey=sk-ant-sid01-abc; cf_clearance=zzz", "sessionKey=sk-ant-sid01-abc; cf_clearance=zzz", false},
		{"empty", "", "", true},
		{"unrelated cookie", "other=1", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cred, err := claudeWebResolveCred(&Request{APIKey: tt.apiKey})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got cred %+v", cred)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveCred: %v", err)
			}
			if cred.cookieHeader != tt.wantHdr {
				t.Errorf("cookieHeader = %q, want %q", cred.cookieHeader, tt.wantHdr)
			}
			if cred.sessionKey != "sk-ant-sid01-abc" {
				t.Errorf("sessionKey = %q", cred.sessionKey)
			}
		})
	}
}
