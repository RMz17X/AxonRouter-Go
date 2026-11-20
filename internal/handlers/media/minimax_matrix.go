package media

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"axonrouter/proxy/internal/handlers/shared"
	"axonrouter/proxy/internal/providers"
)

// MiniMax Code's media tools now live behind the Matrix MCP wrapper on the
// agent host. These helpers keep the wrapper details inside the mmg provider
// path: no generic media code learns a tool name or an MCP envelope.
const (
	miniMaxMatrixPrefix         = "/mavis/api/v1/mcp/"
	miniMaxMatrixImageTool      = "image_synthesize"
	miniMaxMatrixVideoSubmit    = "submit_video_generation"
	miniMaxMatrixVideoQueryTool = "query_video_generation"
	miniMaxMatrixH3Model        = "MiniMax-H3"
	miniMaxMatrixHailuoModel    = "MiniMax-Hailuo-2.3"
	miniMaxMatrixAspectAuto     = "auto"
	miniMaxMatrixResolution1K   = "1K"
	miniMaxMatrixResolution2K   = "2K"
	miniMaxMatrixResolution4K   = "4K"
)

func miniMaxMatrixToolURL(cfg providers.ProviderConfig, connData *shared.ConnectionData, tool string) (string, error) {
	base := cfg.BaseURL
	if connData != nil && connData.BaseURL != "" {
		base = connData.BaseURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid MiniMax agent base URL %q", base)
	}
	return u.Scheme + "://" + u.Host + miniMaxMatrixPrefix + tool, nil
}

// miniMaxMatrixBizError reads the MCP envelope's business code. HTTP 200 does
// not mean the paid job was accepted: a 402 balance refusal arrives as code
// 402 with HTTP 200, and must not be reported as a successful generation.
func miniMaxMatrixBizError(body []byte) (status int, message string, ok bool) {
	var env struct {
		Code     int    `json:"code"`
		Message  string `json:"message"`
		BaseResp *struct {
			StatusCode    int    `json:"status_code"`
			StatusMessage string `json:"status_message"`
		} `json:"base_resp"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return 0, "", false
	}
	code := env.Code
	msg := strings.TrimSpace(env.Message)
	if env.BaseResp != nil {
		if code == 0 && env.BaseResp.StatusCode != 0 {
			code = env.BaseResp.StatusCode
		}
		if msg == "" {
			msg = strings.TrimSpace(env.BaseResp.StatusMessage)
		}
	}
	if code == 0 {
		return 0, "", false
	}
	switch code {
	case 401, 402, 403, 429:
		status = code
	default:
		status = http.StatusBadGateway
	}
	if msg == "" {
		msg = fmt.Sprintf("MiniMax Matrix error (code %d)", code)
	}
	return status, msg, true
}

type miniMaxMatrixImageItem struct {
	Prompt      string `json:"prompt"`
	OutputFile  string `json:"output_file"`
	AspectRatio string `json:"aspect_ratio,omitempty"`
	Resolution  string `json:"resolution,omitempty"`
}

type miniMaxMatrixImageRequest struct {
	Requests []miniMaxMatrixImageItem `json:"requests"`
}

func buildMiniMaxMatrixImageBody(body []byte) ([]byte, error) {
	var req miniMaxImageRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	n := 1
	if req.N != nil {
		n = *req.N
	}
	resolution := miniMaxMatrixImageResolution(req.Size, req.Area)
	aspect := miniMaxMatrixImageAspect(req.Size, req.AspectRatio)
	out := miniMaxMatrixImageRequest{Requests: make([]miniMaxMatrixImageItem, 0, n)}
	for i := 0; i < n; i++ {
		out.Requests = append(out.Requests, miniMaxMatrixImageItem{
			Prompt:      strings.TrimSpace(req.Prompt),
			OutputFile:  fmt.Sprintf("slot_%d.png", i),
			AspectRatio: aspect,
			Resolution:  resolution,
		})
	}
	return json.Marshal(out)
}

func miniMaxMatrixImageResolution(size, area string) string {
	switch area {
	case miniMaxMatrixResolution1K, miniMaxMatrixResolution2K, miniMaxMatrixResolution4K:
		return area
	}
	if size == "" || size == "auto" {
		return ""
	}
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return ""
	}
	w, werr := strconv.Atoi(parts[0])
	h, herr := strconv.Atoi(parts[1])
	if werr != nil || herr != nil || w <= 0 || h <= 0 {
		return ""
	}
	pixels := int64(w) * int64(h)
	switch {
	case pixels <= 1_600_000:
		return miniMaxMatrixResolution1K
	case pixels <= 6_400_000:
		return miniMaxMatrixResolution2K
	default:
		return miniMaxMatrixResolution4K
	}
}

func miniMaxMatrixImageAspect(size, aspect string) string {
	if aspect != "" && aspect != miniMaxMatrixAspectAuto {
		return aspect
	}
	if size == "" || size == "auto" {
		return ""
	}
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return ""
	}
	w, werr := strconv.Atoi(parts[0])
	h, herr := strconv.Atoi(parts[1])
	if werr != nil || herr != nil || w <= 0 || h <= 0 {
		return ""
	}
	divisor := gcd(w, h)
	return strconv.Itoa(w/divisor) + ":" + strconv.Itoa(h/divisor)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}

type miniMaxMatrixImageResult struct {
	Created int64 `json:"created"`
	Data    []struct {
		URL           string `json:"url,omitempty"`
		Size          string `json:"size,omitempty"`
		RevisedPrompt string `json:"revised_prompt,omitempty"`
	} `json:"data"`
}

// translateMiniMaxMatrixImageResult converts one MCP tool response into the
// OpenAI images shape the dashboard and API clients already consume.
func translateMiniMaxMatrixImageResult(body []byte) (int, []byte) {
	if status, msg, ok := miniMaxMatrixBizError(body); ok {
		return status, mustJSON(map[string]any{"error": map[string]any{
			"message": msg, "type": "minimax_matrix_error", "code": status,
		}})
	}
	var env struct {
		SuccessItems []struct {
			OutputURL     string `json:"output_url"`
			Size          string `json:"size"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"success_items"`
		FailedItems []struct {
			Error string `json:"error"`
		} `json:"failed_items"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return http.StatusBadGateway, mustJSON(map[string]any{"error": map[string]any{
			"message": "invalid MiniMax image response", "type": "minimax_matrix_error",
		}})
	}
	if len(env.SuccessItems) == 0 {
		msg := "MiniMax image tool returned no image"
		if len(env.FailedItems) > 0 && env.FailedItems[0].Error != "" {
			msg = env.FailedItems[0].Error
		}
		return http.StatusBadGateway, mustJSON(map[string]any{"error": map[string]any{
			"message": msg, "type": "minimax_matrix_error",
		}})
	}
	result := miniMaxMatrixImageResult{Created: time.Now().Unix()}
	for _, item := range env.SuccessItems {
		result.Data = append(result.Data, struct {
			URL           string `json:"url,omitempty"`
			Size          string `json:"size,omitempty"`
			RevisedPrompt string `json:"revised_prompt,omitempty"`
		}{URL: item.OutputURL, Size: item.Size, RevisedPrompt: item.RevisedPrompt})
	}
	return http.StatusOK, mustJSON(result)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
