package media

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"axonrouter/proxy/internal/providers"
)

// MiniMax H3-Image is synchronous and returns an OpenAI-shaped
// {data:[{url|b64_json,size,revised_prompt}]} body. No response reshape is
// needed; this file only owns the model gate, boundary validation and the
// longer header timeout the upstream contract requires.

const (
	miniMaxImagePromptMaxRunes = 32000
	miniMaxImageMinArea        = 655360
	miniMaxImageMaxArea        = 20000000
	miniMaxImageHeaderTimeout  = 10 * time.Minute
)

var miniMaxH3ImageModels = map[string]bool{
	"MiniMax-H3-Image":           true,
	"MiniMax-H3-Image-Highspeed": true,
}

type miniMaxImageRequest struct {
	Model                 string `json:"model"`
	Prompt                string `json:"prompt"`
	N                     *int   `json:"n"`
	Size                  string `json:"size"`
	AspectRatio           string `json:"aspect_ratio"`
	Area                  string `json:"area"`
	ResponseFormat        string `json:"response_format"`
	OutputFormat          string `json:"output_format"`
	DisablePromptEnhanced *bool  `json:"disable_prompt_enhanced"`
}

func isMiniMaxH3Image(provider, model string) bool {
	return miniMaxH3ImageModels[model] && providers.ResolveAlias(provider) == miniMaxCodeGlobalProvider
}

func isMiniMaxH3ImageRequest(provider, model, endpoint string) bool {
	return isMiniMaxH3Image(provider, model) &&
		(endpoint == "/v1/images/generations" || endpoint == "/images/generations")
}

func validateMiniMaxImageBody(body []byte, model string) error {
	if !miniMaxH3ImageModels[model] {
		return fmt.Errorf("unsupported MiniMax image model %q", model)
	}
	var req miniMaxImageRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return fmt.Errorf("prompt must not be empty")
	}
	if utf8.RuneCountInString(prompt) > miniMaxImagePromptMaxRunes {
		return fmt.Errorf("prompt exceeds %d characters", miniMaxImagePromptMaxRunes)
	}
	if req.N != nil && (*req.N < 1 || *req.N > 10) {
		return fmt.Errorf("n must be 1-10")
	}
	if err := validateMiniMaxImageSize(req.Size, req.AspectRatio, req.Area, req.DisablePromptEnhanced); err != nil {
		return err
	}
	switch req.ResponseFormat {
	case "", "url", "b64_json":
	default:
		return fmt.Errorf("response_format must be url or b64_json")
	}
	switch req.OutputFormat {
	case "", "png", "jpeg", "jpg", "webp":
	default:
		return fmt.Errorf("output_format must be png, jpeg, jpg or webp")
	}
	return nil
}

func validateMiniMaxImageSize(size, aspect, area string, disablePromptEnhanced *bool) error {
	if size != "" && size != "auto" {
		parts := strings.Split(size, "x")
		if len(parts) != 2 {
			return fmt.Errorf("size must be WIDTHxHEIGHT or auto")
		}
		width, werr := strconv.Atoi(parts[0])
		height, herr := strconv.Atoi(parts[1])
		if werr != nil || herr != nil || width <= 0 || height <= 0 {
			return fmt.Errorf("size must be WIDTHxHEIGHT or auto")
		}
		pixels := int64(width) * int64(height)
		if pixels < miniMaxImageMinArea || pixels > miniMaxImageMaxArea {
			return fmt.Errorf("image area must be between %d and %d pixels", miniMaxImageMinArea, miniMaxImageMaxArea)
		}
		if err := validateMiniMaxImageRatio(width, height); err != nil {
			return err
		}
		if aspect != "" || area != "" {
			return fmt.Errorf("aspect_ratio and area cannot be combined with an explicit size")
		}
		return nil
	}
	if aspect != "" && aspect != "auto" {
		parts := strings.Split(aspect, ":")
		if len(parts) != 2 {
			return fmt.Errorf("aspect_ratio must be X:Y or auto")
		}
		x, xerr := strconv.Atoi(parts[0])
		y, yerr := strconv.Atoi(parts[1])
		if xerr != nil || yerr != nil || x < 1 || x > 300 || y < 1 || y > 300 {
			return fmt.Errorf("aspect_ratio components must be 1-300")
		}
		if err := validateMiniMaxImageRatio(x, y); err != nil {
			return err
		}
	}
	if disablePromptEnhanced != nil && *disablePromptEnhanced && (aspect == "" || aspect == "auto") {
		return fmt.Errorf("prompt enhancement disabled requires an explicit aspect_ratio")
	}
	switch area {
	case "", "1K", "2K", "4K":
	default:
		return fmt.Errorf("area must be 1K, 2K or 4K")
	}
	return nil
}

func validateMiniMaxImageRatio(x, y int) error {
	ratio := float64(x) / float64(y)
	if ratio < 1.0/3.0 || ratio > 3.0 {
		return fmt.Errorf("aspect ratio must be between 1:3 and 3:1")
	}
	return nil
}
