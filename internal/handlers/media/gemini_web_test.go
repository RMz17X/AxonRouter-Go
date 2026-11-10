package media

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"axonrouter/proxy/internal/handlers/chat"
)

func TestHandleGeminiWebImage_MissingPrompt(t *testing.T) {
	h := &MediaHandler{}
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"imagen-3"}`))
	rec := httptest.NewRecorder()

	modelInfo := &chat.ModelInfo{
		Provider: "gemini-web",
		Model:    "imagen-3",
	}

	err := h.handleGeminiWebImage(rec, req, []byte(`{"model":"imagen-3"}`), modelInfo)
	if err == nil {
		t.Fatal("expected error for missing prompt, got nil")
	}
	if !strings.Contains(err.Error(), "missing required field: prompt") {
		t.Errorf("expected missing prompt error, got: %v", err)
	}
}
