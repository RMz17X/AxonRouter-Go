package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOpencodePromptCacheKey(t *testing.T) {
	canonical := "ses_0123456789abABCDEFGHIJKLMN"
	hex64 := strings.Repeat("a", 64)
	tests := []struct {
		name    string
		session string
		want    string
	}{
		{"canonical session passes through", canonical, canonical},
		{"cli 64-hex session strips the ses_ prefix", "ses_" + hex64, hex64},
		{"uppercase hex is not the cli shape", "ses_" + strings.Repeat("A", 64), ("ses_" + strings.Repeat("A", 64))[:64]},
		{"foreign long id is capped at 64", strings.Repeat("x", 80), strings.Repeat("x", 64)},
		{"empty stays empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := opencodePromptCacheKey(tt.session); got != tt.want {
				t.Errorf("opencodePromptCacheKey(%q) = %q, want %q", tt.session, got, tt.want)
			}
		})
	}
}

func TestApplyOpenCodeCacheFields(t *testing.T) {
	reqMap := map[string]any{"model": "m"}
	applyOpenCodeCacheFields(reqMap, "ses_0123456789abABCDEFGHIJKLMN")
	if got := reqMap["prompt_cache_key"]; got != "ses_0123456789abABCDEFGHIJKLMN" {
		t.Errorf("prompt_cache_key = %v, want the session id", got)
	}
	if got, ok := reqMap["store"].(bool); !ok || got {
		t.Errorf("store = %v, want false", reqMap["store"])
	}

	empty := map[string]any{"model": "m"}
	applyOpenCodeCacheFields(empty, "")
	if _, ok := empty["prompt_cache_key"]; ok {
		t.Error("an empty session must not install a prompt_cache_key")
	}
	if got, ok := empty["store"].(bool); !ok || got {
		t.Errorf("store = %v, want false even without a session", empty["store"])
	}
}

func TestOpenCodeCacheFieldsRejected(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		wantCache bool
		wantStore bool
	}{
		{
			name:      "gate names prompt_cache_key",
			status:    http.StatusBadRequest,
			body:      `{"error":{"message":"unsupported field: prompt_cache_key"}}`,
			wantCache: true,
		},
		{
			name:      "gate names store",
			status:    http.StatusBadRequest,
			body:      `{"error":{"message":"unsupported field: store"}}`,
			wantStore: true,
		},
		{
			name:      "gate names no field",
			status:    http.StatusBadRequest,
			body:      `{"error":{"message":"unsupported field"}}`,
			wantCache: true,
			wantStore: true,
		},
		{
			name:   "unrelated 400 is left alone",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"Invalid option: reasoning_effort"}}`,
		},
		{
			name:   "429 is not a field refusal",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"message":"unsupported field: prompt_cache_key"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tt.status,
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}
			cache, store := opencodeCacheFieldsRejected(resp)
			if cache != tt.wantCache || store != tt.wantStore {
				t.Errorf("opencodeCacheFieldsRejected() = (%v, %v), want (%v, %v)", cache, store, tt.wantCache, tt.wantStore)
			}
			// The body must survive the inspection so the caller can report it.
			restored, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read restored body: %v", err)
			}
			if string(restored) != tt.body {
				t.Errorf("body not restored: got %q, want %q", restored, tt.body)
			}
		})
	}
}

func TestForwardOpenCodeWithCacheFields_StripsAndReplaysOnce(t *testing.T) {
	reqMap := map[string]any{
		"model":            "m",
		"prompt_cache_key": "k",
		"store":            false,
	}
	var calls int
	var lastBody []byte
	send := func(body []byte) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"unsupported field: prompt_cache_key"}}`)),
			}, nil
		}
		lastBody = body
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	}

	resp, err := forwardOpenCodeWithCacheFields(context.Background(), []byte(`{"model":"m"}`), reqMap, send, send, nil)
	if err != nil {
		t.Fatalf("forwardOpenCodeWithCacheFields: %v", err)
	}
	defer resp.Body.Close()
	if calls != 2 {
		t.Fatalf("expected one replay, got %d calls", calls)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if _, ok := reqMap["prompt_cache_key"]; ok {
		t.Error("prompt_cache_key must be dropped after the refusal")
	}
	if _, ok := reqMap["store"]; !ok {
		t.Error("store must survive a refusal that named only prompt_cache_key")
	}
	if strings.Contains(string(lastBody), "prompt_cache_key") {
		t.Errorf("replay still carries prompt_cache_key: %s", lastBody)
	}
}

func TestForwardOpenCodeWithCacheFields_DoesNotReplayOtherErrors(t *testing.T) {
	var calls int
	send := func(body []byte) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"Invalid option: reasoning_effort"}}`)),
		}, nil
	}
	resp, err := forwardOpenCodeWithCacheFields(context.Background(), []byte(`{"model":"m"}`), map[string]any{"prompt_cache_key": "k"}, send, send, nil)
	if err != nil {
		t.Fatalf("forwardOpenCodeWithCacheFields: %v", err)
	}
	defer resp.Body.Close()
	if calls != 1 {
		t.Fatalf("an unrelated 400 must not be replayed, got %d calls", calls)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want the original 400", resp.StatusCode)
	}
}
