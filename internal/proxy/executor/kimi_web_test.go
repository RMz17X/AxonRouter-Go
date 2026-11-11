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

// fakeKimiSite is a stand-in kimi.moonshot.cn that records what the
// executor sent and answers the spec's refresh + SSE shapes. It
// proves the wire contract — token exchange, chat create, completion
// payload — without a real account.
type fakeKimiSite struct {
	refreshHits  atomic.Int32
	createHits   atomic.Int32
	deleteHits   atomic.Int32
	refreshAuth  string
	createAuth   string
	completion   map[string]any
	completionID string
}

func (f *fakeKimiSite) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/token/refresh", func(w http.ResponseWriter, r *http.Request) {
		f.refreshHits.Add(1)
		f.refreshAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"access_token":"access-1","refresh_token":"refresh-2","expires_in":1800}`))
	})
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		f.createHits.Add(1)
		f.createAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"id":"chat-xyz"}`))
	})
	mux.HandleFunc("POST /api/chat/{id}/completion/stream", func(w http.ResponseWriter, r *http.Request) {
		f.completionID = r.PathValue("id")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &f.completion)
		w.Header().Set("Content-Type", "text/event-stream")
		// The real wire shape, captured live 2026-11-11: the turn
		// opens with furniture events (the "rename" text is the chat
		// title — the prompt's head — and must never reach the client
		// as content), then cmpl deltas, then "done".
		_, _ = w.Write([]byte(
			"data: {\"content\":\"Apa itu internet?\",\"event\":\"req\",\"model\":\"kimi\"}\n\n" +
				"data: {\"event\":\"ping\"}\n\n" +
				"data: {\"event\":\"resp\",\"model\":\"kimi\"}\n\n" +
				"data: {\"event\":\"rename\",\"text\":\"Apa itu internet?\"}\n\n" +
				"data: {\"event\":\"cmpl\",\"text\":\"pikir \",\"view\":\"think\"}\n\n" +
				"data: {\"event\":\"cmpl\",\"text\":\"Internet\",\"view\":\"cmpl\"}\n\n" +
				"data: {\"event\":\"cmpl\",\"text\":\" adalah jaringan\",\"view\":\"cmpl\"}\n\n" +
				"data: {\"event\":\"done\"}\n\n"))
	})
	mux.HandleFunc("DELETE /api/chat/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.deleteHits.Add(1)
		_, _ = w.Write([]byte(`{}`))
	})
	return mux
}

func useFakeKimiSite(t *testing.T, site *fakeKimiSite) {
	t.Helper()
	server := httptest.NewServer(site.handler())
	t.Cleanup(server.Close)

	oldBase, oldFactory := kimiWebBaseURL, kimiWebTransportFactory
	kimiWebBaseURL = server.URL
	kimiWebTransportFactory = func(req *Request) (kimiWebDoer, error) {
		return &kimiWebPlainDoer{}, nil
	}
	t.Cleanup(func() { kimiWebBaseURL, kimiWebTransportFactory = oldBase, oldFactory })
}

func TestForwardKimiWeb_StreamRefreshesAndReuses(t *testing.T) {
	site := &fakeKimiSite{}
	useFakeKimiSite(t, site)

	newReq := func() *Request {
		return &Request{
			Body:         []byte(`{"model":"kimi-latest","messages":[{"role":"user","content":"Apa itu internet?"}]}`),
			APIKey:       "refresh-1",
			IsStream:     true,
			ConnectionID: "conn-kimi-stream",
		}
	}

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		if err := ForwardKimiWeb(rec, newReq()); err != nil {
			t.Fatalf("ForwardKimiWeb #%d: %v", i, err)
		}
		out := rec.Body.String()
		for _, want := range []string{`"content":"Internet"`, `"content":" adalah jaringan"`, `"reasoning_content":"pikir "`, `"finish_reason":"stop"`, "data: [DONE]"} {
			if !strings.Contains(out, want) {
				t.Errorf("call #%d stream output missing %q:\n%s", i, want, out)
			}
		}
		// The chat-title furniture event must not leak as content:
		// the prompt may appear in the stream only inside the req
		// echo, which the client never sees.
		if strings.Contains(out, `"content":"Apa itu internet?"`) {
			t.Errorf("call #%d leaked the chat title as content:\n%s", i, out)
		}
	}

	// The refresh token was exchanged once; the second turn rode the
	// cached access token.
	if site.refreshHits.Load() != 1 {
		t.Errorf("refresh hits = %d, want 1", site.refreshHits.Load())
	}
	if site.refreshAuth != "Bearer refresh-1" {
		t.Errorf("refresh Authorization = %q", site.refreshAuth)
	}
	if site.createAuth != "Bearer access-1" {
		t.Errorf("create Authorization = %q", site.createAuth)
	}
	if site.createHits.Load() != 2 || site.completionID != "chat-xyz" {
		t.Errorf("create hits = %d, completion chat = %q", site.createHits.Load(), site.completionID)
	}
	if got := site.completion["use_search"]; got != false {
		t.Errorf("use_search = %v, want false for kimi-latest (search is a media service, not a chat model)", got)
	}
	if got := site.completion["model"]; got != "kimi" {
		t.Errorf("completion model = %v", got)
	}
	msgs, _ := site.completion["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("completion messages = %v", site.completion["messages"])
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "user" || first["content"] != "Apa itu internet?" {
		t.Errorf("first message = %v", first)
	}

	deadline := time.Now().Add(2 * time.Second)
	for site.deleteHits.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if site.deleteHits.Load() != 2 {
		t.Errorf("delete hits = %d, want 2", site.deleteHits.Load())
	}
}

func TestForwardKimiWeb_CollectUsesStoredAccessToken(t *testing.T) {
	site := &fakeKimiSite{}
	useFakeKimiSite(t, site)

	rec := httptest.NewRecorder()
	req := &Request{
		Body:   []byte(`{"model":"kimi-latest","messages":[{"role":"system","content":"Jawab singkat."},{"role":"user","content":"Halo"}]}`),
		APIKey: "refresh-9",
		ConnData: map[string]any{
			"accessToken":    "stored-access",
			"tokenExpiresAt": float64(time.Now().Unix() + 900),
		},
		ConnectionID: "conn-kimi-collect",
	}
	if err := ForwardKimiWeb(rec, req); err != nil {
		t.Fatalf("ForwardKimiWeb: %v", err)
	}
	if site.refreshHits.Load() != 0 {
		t.Errorf("refresh ran despite a live stored access token")
	}
	if site.createAuth != "Bearer stored-access" {
		t.Errorf("create Authorization = %q", site.createAuth)
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not a chat.completion: %v (%s)", err, rec.Body.String())
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "Internet adalah jaringan" {
		t.Errorf("collected content = %+v", resp.Choices)
	}
	if resp.Choices[0].Message.ReasoningContent != "pikir " {
		t.Errorf("reasoning_content = %q", resp.Choices[0].Message.ReasoningContent)
	}
	// The system turn was folded into the first user message.
	msgs, _ := site.completion["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", site.completion["messages"])
	}
	first, _ := msgs[0].(map[string]any)
	if first["content"] != "Jawab singkat.\n\nHalo" {
		t.Errorf("folded first message = %v", first["content"])
	}
	if got := site.completion["use_search"]; got != false {
		t.Errorf("use_search = %v, want false for kimi-latest", got)
	}
}

func TestKimiWebResolveCred(t *testing.T) {
	tests := []struct {
		name   string
		apiKey string
		want   string
	}{
		{"bare", "eyJ-refresh-token", "eyJ-refresh-token"},
		{"pair", "refresh_token=eyJ-refresh-token", "eyJ-refresh-token"},
		{"json", `{"refreshToken":"eyJ-refresh-token"}`, "eyJ-refresh-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cred, err := kimiWebResolveCred(&Request{APIKey: tt.apiKey})
			if err != nil {
				t.Fatalf("ResolveCred: %v", err)
			}
			if cred.refreshToken != tt.want {
				t.Errorf("refreshToken = %q, want %q", cred.refreshToken, tt.want)
			}
		})
	}
	if _, err := kimiWebResolveCred(&Request{APIKey: ""}); err == nil {
		t.Errorf("empty credential: expected an error")
	}
}
