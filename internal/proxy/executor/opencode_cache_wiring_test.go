package executor

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"axonrouter/proxy/internal/providers"
)

// The free lane must send the same prompt-cache pair the official CLI does:
// one session id in the headers and in prompt_cache_key, store:false, and an
// output budget large enough to survive the reasoning phase.
func TestForwardOpencode_SendsPromptCacheFieldsAndRaisesBudget(t *testing.T) {
	var sent map[string]any
	var session string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session = r.Header.Get("x-opencode-session")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Errorf("parse upstream body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"msg_123","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	req := &Request{
		Client: srv.Client(),
		Config: &providers.ProviderConfig{BaseURL: srv.URL},
		Body: []byte(`{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"}],` +
			`"max_tokens":50,"reasoning_effort":"high"}`),
	}
	if err := ForwardOpencode(rec, req); err != nil {
		t.Fatalf("ForwardOpencode: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	if session == "" {
		t.Fatal("no x-opencode-session header")
	}
	if got, _ := sent["prompt_cache_key"].(string); got != session {
		t.Errorf("prompt_cache_key = %q, want the header session %q", got, session)
	}
	if got, ok := sent["store"].(bool); !ok || got {
		t.Errorf("store = %v, want false", sent["store"])
	}
	if _, ok := sent["max_tokens"]; ok {
		t.Error("max_tokens must be rewritten into max_completion_tokens")
	}
	if got, _ := sent["max_completion_tokens"].(float64); got != 5120 {
		t.Errorf("max_completion_tokens = %v, want 5120", sent["max_completion_tokens"])
	}
}

// A gate that refuses the cache pair must cost one retry, not the request.
func TestForwardOpencode_StripsRefusedCacheFieldAndReplays(t *testing.T) {
	var calls int
	var secondBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if calls == 1 {
			if !strings.Contains(string(body), "prompt_cache_key") {
				t.Errorf("first attempt should carry prompt_cache_key: %s", body)
			}
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"unsupported field: prompt_cache_key"}}`))
			return
		}
		secondBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"msg_123","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	req := &Request{
		Client: srv.Client(),
		Config: &providers.ProviderConfig{BaseURL: srv.URL},
		Body:   []byte(`{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"}]}`),
	}
	if err := ForwardOpencode(rec, req); err != nil {
		t.Fatalf("ForwardOpencode: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected exactly one replay, got %d attempts", calls)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 after the replay", rec.Code)
	}
	if strings.Contains(secondBody, "prompt_cache_key") {
		t.Errorf("replay still carries prompt_cache_key: %s", secondBody)
	}
	if !strings.Contains(secondBody, `"store":false`) {
		t.Errorf("replay should keep store:false: %s", secondBody)
	}
}
