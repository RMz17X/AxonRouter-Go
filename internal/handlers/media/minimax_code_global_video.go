package media

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode/utf8"

	"axonrouter/proxy/internal/handlers/shared"
	"axonrouter/proxy/internal/providers"
)

// The operator directed video traffic through the existing MiniMax Code global
// lane (mmg) instead of a separate provider. These helpers keep that decision
// contained in this file: no generic router code learns MiniMax endpoint or
// payload details, and no other provider inherits them.

const (
	miniMaxCodeGlobalProvider = "minimax-code-global"
	miniMaxVideoCreatePath    = "/v2/video_generation"
	miniMaxVideoQueryPath     = "/v2/query/video_generation/"
	miniMaxVideoPollSeconds   = 10
	miniMaxPromptMaxRunes     = 7000
	miniMaxVideoMaxMediaItems = 12
)

type miniMaxVideoLimits struct {
	resolutions map[string]bool
	minDuration int
	maxDuration int
}

type miniMaxResolution string

const (
	miniMaxRes480P miniMaxResolution = "480P"
	miniMaxRes768P miniMaxResolution = "768P"
	miniMaxRes2K   miniMaxResolution = "2K"
)

func miniMaxResolutionSet(values ...miniMaxResolution) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[string(v)] = true
	}
	return set
}

var miniMaxVideoModels = map[string]miniMaxVideoLimits{
	"MiniMax-H3":           {miniMaxResolutionSet(miniMaxRes768P, miniMaxRes2K), 4, 15},
	"MiniMax-H3-Highspeed": {miniMaxResolutionSet(miniMaxRes480P, miniMaxRes768P), 4, 15},
	"MiniMax-H3-Lite":      {miniMaxResolutionSet(miniMaxRes480P, miniMaxRes768P), 4, 15},
	"MiniMax-H3-Max":       {miniMaxResolutionSet(miniMaxRes480P, miniMaxRes768P), 5, 15},
}

type miniMaxMediaRef struct {
	URL string `json:"url"`
}

type miniMaxContentItem struct {
	Type     string           `json:"type"`
	Text     string           `json:"text,omitempty"`
	Role     string           `json:"role,omitempty"`
	ImageURL *miniMaxMediaRef `json:"image_url,omitempty"`
	VideoURL *miniMaxMediaRef `json:"video_url,omitempty"`
	AudioURL *miniMaxMediaRef `json:"audio_url,omitempty"`
}

type miniMaxCreateRequest struct {
	Model         string               `json:"model"`
	Content       []miniMaxContentItem `json:"content"`
	Prompt        string               `json:"prompt,omitempty"`
	Duration      int                  `json:"duration"`
	Resolution    string               `json:"resolution"`
	Ratio         string               `json:"ratio,omitempty"`
	GenerateAudio *bool                `json:"generate_audio,omitempty"`
}

func isMiniMaxCodeGlobalVideoProvider(provider string) bool {
	return providers.ResolveAlias(provider) == miniMaxCodeGlobalProvider
}

// miniMaxVideoCreateURL returns the platform video endpoint. MiniMax Code's
// stored baseUrl names the chat agent host and must not redirect video there;
// only an explicit /v2/... override is honoured (useful for mock upstreams).
func miniMaxVideoCreateURL(cfg providers.ProviderConfig, connData *shared.ConnectionData) string {
	if connData != nil && connData.BaseURL != "" {
		if u, err := url.Parse(connData.BaseURL); err == nil && u.Host != "" && strings.Contains(u.Path, "/v2/") {
			return strings.TrimRight(connData.BaseURL, "/")
		}
	}
	base := strings.TrimRight(cfg.VideoURL, "/")
	if base == "" {
		return "https://api.minimax.io" + miniMaxVideoCreatePath
	}
	return base
}

func miniMaxVideoQueryURL(cfg providers.ProviderConfig, connData *shared.ConnectionData, taskID string) (string, error) {
	u, err := url.Parse(miniMaxVideoCreateURL(cfg, connData))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid MiniMax video endpoint: %q", cfg.VideoURL)
	}
	return u.Scheme + "://" + u.Host + miniMaxVideoQueryPath + url.PathEscape(taskID), nil
}

func miniMaxVideoContentURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("MiniMax returned an invalid content URL")
	}
	return u.String(), nil
}

func publicVideoMediaURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast()
	}
	return true
}

func validateMiniMaxVideoBody(raw []byte, model string) ([]byte, error) {
	limits, ok := miniMaxVideoModels[model]
	if !ok {
		return nil, fmt.Errorf("unsupported MiniMax video model %q", model)
	}
	var req miniMaxCreateRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	items := req.Content
	if strings.TrimSpace(req.Prompt) != "" {
		if len(items) != 0 {
			return nil, fmt.Errorf("use either prompt or content, not both")
		}
		items = []miniMaxContentItem{{Type: "text", Text: req.Prompt}}
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("prompt or content is required")
	}
	if err := validateMiniMaxContent(items); err != nil {
		return nil, err
	}
	if !limits.resolutions[req.Resolution] {
		return nil, fmt.Errorf("resolution %q is not supported by %s", req.Resolution, model)
	}
	if req.Duration < limits.minDuration || req.Duration > limits.maxDuration {
		return nil, fmt.Errorf("duration must be %d-%d seconds for %s", limits.minDuration, limits.maxDuration, model)
	}
	audio := true
	if req.GenerateAudio != nil {
		audio = *req.GenerateAudio
	}
	out := miniMaxCreateRequest{
		Model:         model, // URL wins: a body cannot hijack the routed model.
		Content:       items,
		Duration:      req.Duration,
		Resolution:    req.Resolution,
		Ratio:         req.Ratio,
		GenerateAudio: &audio,
	}
	return json.Marshal(out)
}

func validateMiniMaxContent(items []miniMaxContentItem) error {
	var textItems, runes, first, last, refImages, refVideos, refAudio, media int
	for _, item := range items {
		switch item.Type {
		case "text":
			if strings.TrimSpace(item.Text) == "" {
				return fmt.Errorf("text content must not be empty")
			}
			textItems++
			runes += utf8.RuneCountInString(item.Text)
		case "image_url", "video_url", "audio_url":
			ref := miniMaxItemRef(item)
			if ref == nil || !publicVideoMediaURL(ref.URL) {
				return fmt.Errorf("%s requires a public http(s) url", item.Type)
			}
			media++
			if media > miniMaxVideoMaxMediaItems {
				return fmt.Errorf("too many media references (max %d)", miniMaxVideoMaxMediaItems)
			}
			switch item.Role {
			case "first_frame":
				if item.Type != "image_url" {
					return fmt.Errorf("invalid first_frame reference")
				}
				first++
				if first > 1 {
					return fmt.Errorf("too many first_frame references")
				}
			case "last_frame":
				if item.Type != "image_url" {
					return fmt.Errorf("invalid last_frame reference")
				}
				last++
				if last > 1 {
					return fmt.Errorf("too many last_frame references")
				}
			case "reference_image":
				if item.Type != "image_url" {
					return fmt.Errorf("invalid reference_image reference")
				}
				refImages++
				if refImages > 9 {
					return fmt.Errorf("too many reference_image references")
				}
			case "reference_video":
				if item.Type != "video_url" {
					return fmt.Errorf("invalid reference_video reference")
				}
				refVideos++
				if refVideos > 3 {
					return fmt.Errorf("too many reference_video references")
				}
			case "reference_audio":
				if item.Type != "audio_url" {
					return fmt.Errorf("invalid reference_audio reference")
				}
				refAudio++
				if refAudio > 3 {
					return fmt.Errorf("too many reference_audio references")
				}
			default:
				return fmt.Errorf("unknown media role %q", item.Role)
			}
		default:
			return fmt.Errorf("unknown content type %q", item.Type)
		}
	}
	if textItems != 1 {
		return fmt.Errorf("exactly one text prompt is required")
	}
	if runes > miniMaxPromptMaxRunes {
		return fmt.Errorf("prompt exceeds %d characters", miniMaxPromptMaxRunes)
	}
	if (first > 0 || last > 0) && (refImages > 0 || refVideos > 0 || refAudio > 0) {
		return fmt.Errorf("frame and reference media cannot be mixed")
	}
	return nil
}

func miniMaxItemRef(item miniMaxContentItem) *miniMaxMediaRef {
	switch item.Type {
	case "image_url":
		return item.ImageURL
	case "video_url":
		return item.VideoURL
	case "audio_url":
		return item.AudioURL
	}
	return nil
}

type miniMaxPollPayload struct {
	TaskID  string `json:"task_id"`
	Status  string `json:"status"`
	FileID  string `json:"file_id"`
	Content struct {
		URL string `json:"url"`
	} `json:"content"`
	Error    *miniMaxTaskError   `json:"error"`
	BaseResp *miniMaxBaseResp    `json:"base_resp"`
	Task     *miniMaxPollPayload `json:"task"`
}

type miniMaxTaskError struct {
	Message string `json:"message"`
}

type miniMaxBaseResp struct {
	StatusCode    int    `json:"status_code"`
	StatusMessage string `json:"status_message"`
}

func parseMiniMaxVideoPoll(body []byte) (miniMaxPollPayload, error) {
	var p miniMaxPollPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return p, fmt.Errorf("invalid MiniMax poll response: %w", err)
	}
	if p.Task != nil {
		nested := *p.Task
		if p.Status == "" {
			p = nested
		}
	}
	if p.Status == "" && p.TaskID == "" && p.FileID == "" {
		return p, fmt.Errorf("MiniMax poll response has no task state")
	}
	return p, nil
}

func (p miniMaxPollPayload) terminal() bool {
	switch strings.ToLower(p.Status) {
	case "succeeded", "failed", "cancelled":
		return true
	}
	return false
}

func (p miniMaxPollPayload) errorText() string {
	if p.Error != nil && p.Error.Message != "" {
		return p.Error.Message
	}
	if p.BaseResp != nil && p.BaseResp.StatusMessage != "" {
		return p.BaseResp.StatusMessage
	}
	return "MiniMax video task " + p.Status
}
