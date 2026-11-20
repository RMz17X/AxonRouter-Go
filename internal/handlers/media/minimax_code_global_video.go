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
	miniMaxRes480P  miniMaxResolution = "480P"
	miniMaxRes768P  miniMaxResolution = "768P"
	miniMaxRes1080P miniMaxResolution = "1080P"
	miniMaxRes2K    miniMaxResolution = "2K"
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
	"MiniMax-Hailuo-2.3":   {miniMaxResolutionSet(miniMaxRes768P, miniMaxRes1080P), 6, 10},
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

type miniMaxMatrixMedia struct {
	URL string `json:"url"`
}

type miniMaxMatrixVideoRequest struct {
	Model           string               `json:"model"`
	Prompt          string               `json:"prompt"`
	Duration        int                  `json:"duration"`
	Resolution      string               `json:"resolution,omitempty"`
	Ratio           string               `json:"ratio,omitempty"`
	InputImage      *miniMaxMatrixMedia  `json:"input_image,omitempty"`
	LastFrameImage  *miniMaxMatrixMedia  `json:"last_frame_image,omitempty"`
	ReferenceImages []miniMaxMatrixMedia `json:"reference_images,omitempty"`
	ReferenceVideos []miniMaxMatrixMedia `json:"reference_videos,omitempty"`
	ReferenceAudios []miniMaxMatrixMedia `json:"reference_audios,omitempty"`
}

type miniMaxMatrixVideoQuery struct {
	Model  string `json:"model"`
	TaskID string `json:"task_id"`
}

// MiniMax Code video traffic is served by the Matrix MCP tools on the agent
// host. The stored chat baseUrl supplies the host; a connection override may
// point at a local mock, but it never redirects to the platform REST API.
func miniMaxVideoCreateURL(cfg providers.ProviderConfig, connData *shared.ConnectionData) (string, error) {
	return miniMaxMatrixToolURL(cfg, connData, miniMaxMatrixVideoSubmit)
}

func miniMaxVideoQueryURL(cfg providers.ProviderConfig, connData *shared.ConnectionData, _ string) (string, error) {
	return miniMaxMatrixToolURL(cfg, connData, miniMaxMatrixVideoQueryTool)
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
	applyMiniMaxVideoDefaults(&req, model, limits)
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
	out, err := buildMiniMaxMatrixVideoRequest(req, items, model)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

// Matrix declares a default resolution but still requires a duration, so a
// minimal OpenAI-style request needs router-side defaults. Pick the shortest
// duration the model allows and a resolution the model actually accepts.
func applyMiniMaxVideoDefaults(req *miniMaxCreateRequest, model string, limits miniMaxVideoLimits) {
	if req.Duration == 0 {
		req.Duration = limits.minDuration
	}
	if req.Resolution != "" {
		return
	}
	switch model {
	case "MiniMax-H3":
		req.Resolution = string(miniMaxRes2K)
	case "MiniMax-Hailuo-2.3":
		req.Resolution = string(miniMaxRes768P)
	default:
		if limits.resolutions[string(miniMaxRes768P)] {
			req.Resolution = string(miniMaxRes768P)
		}
	}
}

func buildMiniMaxMatrixVideoRequest(req miniMaxCreateRequest, items []miniMaxContentItem, model string) (miniMaxMatrixVideoRequest, error) {
	out := miniMaxMatrixVideoRequest{
		Model:      miniMaxMatrixVideoModel(model),
		Duration:   req.Duration,
		Resolution: miniMaxMatrixVideoResolution(req.Resolution),
		Ratio:      req.Ratio,
	}
	for _, item := range items {
		if item.Type == "text" {
			out.Prompt = strings.TrimSpace(item.Text)
			continue
		}
		ref := miniMaxItemRef(item)
		if ref == nil {
			return out, fmt.Errorf("invalid %s reference", item.Type)
		}
		media := miniMaxMatrixMedia{URL: ref.URL}
		switch item.Type {
		case "image_url":
			switch item.Role {
			case "first_frame":
				out.InputImage = &media
			case "last_frame":
				out.LastFrameImage = &media
			case "reference_image":
				out.ReferenceImages = append(out.ReferenceImages, media)
			}
		case "video_url":
			out.ReferenceVideos = append(out.ReferenceVideos, media)
		case "audio_url":
			out.ReferenceAudios = append(out.ReferenceAudios, media)
		}
	}
	if out.Prompt == "" {
		return out, fmt.Errorf("prompt is required")
	}
	if out.InputImage != nil || out.LastFrameImage != nil {
		out.Ratio = "adaptive"
	}
	return out, nil
}
func miniMaxMatrixVideoModel(model string) string {
	if model == "MiniMax-Hailuo-2.3" {
		return miniMaxMatrixHailuoModel
	}
	return miniMaxMatrixH3Model
}

// Matrix exposes only 768P and 2K for H3. Lower-variant aliases that accept
// 480P are promoted to 768P, the nearest accepted resolution, instead of
// sending a payload the MCP tool would reject.
func miniMaxMatrixVideoResolution(resolution string) string {
	if resolution == string(miniMaxRes480P) {
		return string(miniMaxRes768P)
	}
	return resolution
}

func buildMiniMaxMatrixVideoQuery(model, taskID string) ([]byte, error) {
	return json.Marshal(miniMaxMatrixVideoQuery{Model: miniMaxMatrixVideoModel(model), TaskID: taskID})
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
	TaskID   string `json:"task_id"`
	Status   string `json:"status"`
	FileID   string `json:"file_id"`
	VideoURL string `json:"video_url"` // Matrix query tool returns this directly.
	Code     int    `json:"code"`
	Message  string `json:"message"`
	Content  struct {
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
	if p.Code != 0 && p.Message != "" && p.Error == nil {
		if p.Status == "" {
			p.Status = "failed"
		}
		p.Error = &miniMaxTaskError{Message: p.Message}
	}
	if p.Status == "" && p.Code == 0 && p.VideoURL != "" {
		p.Status = "succeeded"
	}
	if p.Status == "" && p.TaskID == "" && p.FileID == "" && p.VideoURL == "" {
		return p, fmt.Errorf("MiniMax poll response has no task state")
	}
	return p, nil
}

func (p miniMaxPollPayload) contentURL() string {
	if p.VideoURL != "" {
		return p.VideoURL
	}
	return p.Content.URL
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
