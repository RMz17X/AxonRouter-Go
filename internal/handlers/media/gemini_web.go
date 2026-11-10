package media

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"strings"
	"time"

	"axonrouter/proxy/internal/handlers/chat"
	"axonrouter/proxy/internal/proxy/executor"
)

// handleGeminiWebImage serves /v1/images/generations and /images/edits for provider gemini-web.
func (h *MediaHandler) handleGeminiWebImage(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
	var req struct {
		Prompt         string   `json:"prompt"`
		Model          string   `json:"model"`
		Image          string   `json:"image"`
		Images         []string `json:"images"`
		N              int      `json:"n"`
		Size           string   `json:"size"`
		ResponseFormat string   `json:"response_format"`
	}
	_ = json.Unmarshal(body, &req)
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return fmt.Errorf("missing required field: prompt")
	}

	cookie, err := h.geminiWebCookie(r.Context(), modelInfo.ConnectionID, modelInfo.Model)
	if err != nil {
		return err
	}

	n := req.N
	if n < 1 {
		n = 1
	}
	if n > 4 {
		n = 4
	}

	inputImage := req.Image
	if inputImage == "" && len(req.Images) > 0 {
		inputImage = req.Images[0]
	}

	results, err := executor.GenerateGeminiWebImage(r.Context(), cookie, prompt, n, inputImage)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		return fmt.Errorf("gemini-web returned no generated image")
	}

	data := make([]map[string]any, 0, len(results))
	for _, res := range results {
		entry := map[string]any{
			"revised_prompt": prompt,
		}
		if req.ResponseFormat == "url" && res.URL != "" {
			entry["url"] = res.URL
		} else {
			entry["b64_json"] = res.B64
		}
		data = append(data, entry)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	out, err := json.Marshal(map[string]any{
		"created": time.Now().Unix(),
		"data":    data,
	})
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	return err
}

// geminiWebCookie resolves an active gemini-web connection and returns its cookie header.
func (h *MediaHandler) geminiWebCookie(ctx context.Context, preferredConnID, model string) (string, error) {
	conn, connData, err := h.ChatH.GetBestConnectionWithContext(ctx, "gemini-web", preferredConnID, nil, model)
	if (err != nil || connData == nil) && model != "" {
		// Fallback to unconstrained connection lookup if model-specific constraint failed
		conn, connData, err = h.ChatH.GetBestConnectionWithContext(ctx, "gemini-web", preferredConnID, nil, "")
	}
	if err != nil || connData == nil {
		return "", fmt.Errorf("no active connection for gemini-web: %w", err)
	}
	if h.Repo != nil && conn != nil {
		_ = h.Repo.UpdateConnectionLastUsed(conn.ID)
	}
	rawAPIKey := chat.ExtractAPIKey(connData)
	cookie := executor.GeminiWebCookieHeader(rawAPIKey)
	return cookie, nil
}
