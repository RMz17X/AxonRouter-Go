package media

// Batch 10 item 4 — video create / poll / content handlers,
// ported from AxonRouter src/sse/handlers/videoGeneration.js
// over the proxy core in video_core.go.
//
// Pinning: a create answers with the header
// x-axonrouter-connection-id naming the connection that created
// the job; polls echo it back as x-connection-id and are served
// by that same connection (its own baseUrl and credentials). A
// poll without a pin resolves ?provider= when it names a video
// provider, else Axon's default provider (xai).
//
// Create rotation follows Axon: only 401/403/429 rotate to the
// next connection (the core already refreshed once before
// surfacing them); a 5xx is returned to the client, never
// re-sent — the job may exist upstream.
//
// Everything outside the ported core's scope keeps the existing
// media lane (forwardMediaRequest): combos, the special web
// providers (uc-web, veoaifree-web, grok-web — none of them video
// providers in Axon's registry), and providers with no video
// config (e.g. openai/sora).

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// videoDefaultProvider is Axon's DEFAULT_VIDEO_PROVIDER.
const videoDefaultProvider = "xai"

// videoMaxFallbackAttempts is Axon's MAX_FALLBACK_ATTEMPTS for
// video create rotation.
const videoMaxFallbackAttempts = 10

// HandleVideoGet handles GET /v1/videos/{id} — a live proxy of
// the upstream job status through the creating connection (the
// pre-batch-10 canned {status:"completed"} answer is gone).
func (h *MediaHandler) HandleVideoGet(w http.ResponseWriter, r *http.Request) {
	h.handleVideoGet(w, r, false)
}

// HandleVideoContent handles GET /v1/videos/{id}/content — the
// job's media bytes, streamed from the creating connection.
func (h *MediaHandler) HandleVideoContent(w http.ResponseWriter, r *http.Request) {
	h.handleVideoGet(w, r, true)
}

func (h *MediaHandler) handleVideoGet(w http.ResponseWriter, r *http.Request, content bool) {
	id := r.PathValue("id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing video request id")
		return
	}

	provider := videoDefaultProvider
	pinned := r.Header.Get("x-connection-id")
	if pinned != "" {
		conn, err := h.Repo.GetProviderConnectionByID(pinned)
		if err != nil || conn == nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "Connection not found: "+pinned)
			return
		}
		if _, ok := videoConfigFor(conn.Provider); !ok {
			handlerutil.WriteJSONError(w, http.StatusBadRequest,
				fmt.Sprintf("Provider '%s' does not support video generation", conn.Provider))
			return
		}
		provider = conn.Provider
	} else if q := r.URL.Query().Get("provider"); q != "" {
		if _, ok := videoConfigFor(q); ok {
			provider = q
		} else if canonical, ok := providers.ProviderAliasMap[q]; ok {
			if _, ok := videoConfigFor(canonical); ok {
				provider = canonical
			}
		}
	}

	conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), provider, pinned, nil, "")
	if err != nil || conn == nil || connData == nil || (pinned != "" && conn.ID != pinned) {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "No credentials for provider: "+provider)
		return
	}

	cfg := h.videoRegistryConfig(provider, connData)
	start := time.Now()
	res := h.videoProxy(r.Context(), provider, cfg, conn, connData, videoProxyParams{
		RequestID: id, Content: content,
	})
	h.logVideoUsage(r, provider, "video", conn, connData, time.Since(start))
	if res.ErrText != "" {
		handlerutil.WriteJSONError(w, res.Status, res.ErrText)
		return
	}
	h.Repo.UpdateConnectionLastUsed(conn.ID)
	writeVideoResult(w, res, conn.ID)
}

// handleVideoCreate serves POST /v1/videos/{generations,edits,
// extensions} for the providers in the core's scope.
func (h *MediaHandler) handleVideoCreate(w http.ResponseWriter, r *http.Request, body []byte, action, path string) {
	contentType := r.Header.Get("Content-Type")
	model, hasModel := extractVideoModel(body, contentType)
	effectiveModel := model
	if effectiveModel == "" {
		effectiveModel = "sora" // the media lane's standing default
	}
	if !h.ChatH.EnforceModelAccess(w, r, effectiveModel) {
		return
	}
	if _, answered := guardMediaInput(w, contentType, body); answered {
		return
	}
	info, err := h.ChatH.ResolveModel(effectiveModel)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	provider := info.Provider

	// Out-of-scope shapes keep the existing lane.
	if len(info.ComboModels) > 0 || (action == "generations" && isSpecialVideoProvider(provider)) {
		h.forwardMediaRequest(w, r, body, "sora", path)
		return
	}
	if _, ok := videoConfigFor(provider); !ok {
		h.forwardMediaRequest(w, r, body, "sora", path)
		return
	}

	// OpenRouter adapter gates (Axon answers these plan errors
	// at 400 before any upstream call).
	if provider == "openrouter" {
		if action != "generations" {
			handlerutil.WriteJSONError(w, http.StatusBadRequest,
				fmt.Sprintf("[openrouter] OpenRouter video supports 'generations' only (got '%s')", action))
			return
		}
		if contentType != "" && !strings.Contains(contentType, "application/json") {
			handlerutil.WriteJSONError(w, http.StatusBadRequest,
				"[openrouter] OpenRouter video requires an application/json body")
			return
		}
	}

	// Strip the provider prefix from the model field, the only
	// body rewrite Axon performs (and only when it changes).
	finalBody := body
	if hasModel && model != info.Model {
		if strings.HasPrefix(contentType, "multipart/") {
			if rewritten, rerr := rewriteMultipartModelPart(body, contentType, model, info.Model); rerr == nil {
				finalBody = rewritten
			}
		} else {
			finalBody = handlerutil.UpdateModelInBody(body, info.Model)
		}
	}

	preferred := r.Header.Get("x-connection-id")
	if preferred == "" {
		preferred = r.Header.Get("x-provider-connection-id")
	}
	if preferred == "" {
		preferred = info.ConnectionID
	}

	var exclude []string
	lastStatus, lastError := 0, ""
	for attempt := 0; attempt < videoMaxFallbackAttempts; attempt++ {
		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), provider, preferred, exclude, info.Model)
		if err != nil || conn == nil || connData == nil {
			if attempt == 0 {
				handlerutil.WriteJSONError(w, http.StatusServiceUnavailable, "No credentials for provider: "+provider)
				return
			}
			break
		}
		cfg := h.videoRegistryConfig(provider, connData)
		start := time.Now()
		res := h.videoProxy(r.Context(), provider, cfg, conn, connData, videoProxyParams{
			Action: action, Body: finalBody, ContentType: contentType,
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
		})
		h.logVideoUsage(r, provider, info.Model, conn, connData, time.Since(start))
		if res.ErrText == "" {
			h.Repo.UpdateConnectionLastUsed(conn.ID)
			writeVideoResult(w, res, conn.ID)
			return
		}
		lastStatus, lastError = res.Status, res.ErrText
		if res.Status == http.StatusUnauthorized || res.Status == http.StatusForbidden || res.Status == http.StatusTooManyRequests {
			exclude = append(exclude, conn.ID)
			continue
		}
		// A 5xx (or any other failure) is returned, never re-sent.
		handlerutil.WriteJSONError(w, res.Status, res.ErrText)
		return
	}
	if lastStatus == 0 {
		lastStatus = http.StatusServiceUnavailable
	}
	if lastError == "" {
		lastError = "All accounts unavailable"
	}
	handlerutil.WriteJSONError(w, lastStatus, lastError)
}

// videoRegistryConfig resolves the provider config for one
// connection (custom baseUrl clones keep their VideoURL), falling
// back to the bare registry entry when resolution errors.
func (h *MediaHandler) videoRegistryConfig(provider string, connData *shared.ConnectionData) providers.ProviderConfig {
	if cfg, err := h.ChatH.GetProviderConfig(provider, connData); err == nil && cfg != nil {
		return *cfg
	}
	cfg, _ := videoConfigFor(provider)
	return cfg
}

// logVideoUsage records one video call in the gateway's usage
// history — the closest Go counterpart of Axon's per-call
// saveCapabilityUsage (Axon keeps a separate capability ledger;
// this gateway's usage path is LogUsage).
func (h *MediaHandler) logVideoUsage(r *http.Request, provider, model string, conn *models.ProviderConnection, connData *shared.ConnectionData, latency time.Duration) {
	token := connData.AccessToken
	if token == "" {
		token = connData.APIKey
	}
	h.ChatH.LogUsage(r.Context(), &shared.UsageLogInfo{
		Provider: provider, Model: model, ConnectionID: conn.ID,
		APIKey: token, Endpoint: "/v1/videos",
	}, nil, latency.Milliseconds(), nil, nil)
}

// writeVideoResult writes a successful core response: JSON
// verbatim for creates/polls, streamed bytes for content, always
// with Axon's CORS header and the pinning header naming the
// serving connection.
func writeVideoResult(w http.ResponseWriter, res *videoUpstreamResponse, connectionID string) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if connectionID != "" {
		w.Header().Set("x-axonrouter-connection-id", connectionID)
	}
	if res.Stream != nil {
		defer res.Stream.Close()
		w.Header().Set("Content-Type", res.ContentType)
		if res.ContentDisposition != "" {
			w.Header().Set("Content-Disposition", res.ContentDisposition)
		}
		if res.ContentLength >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(res.ContentLength, 10))
		}
		w.WriteHeader(res.Status)
		_, _ = io.Copy(w, res.Stream)
		return
	}
	w.Header().Set("Content-Type", res.ContentType)
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
}

// extractVideoModel reads the model field of a create body —
// the JSON field, or the multipart form part (Axon resolves the
// model from the parsed body the same way before forwarding the
// raw bytes untouched).
func extractVideoModel(body []byte, contentType string) (string, bool) {
	if strings.HasPrefix(contentType, "multipart/") {
		_, params, err := mime.ParseMediaType(contentType)
		if err != nil || params["boundary"] == "" {
			return "", false
		}
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, err := reader.NextPart()
			if err != nil {
				return "", false
			}
			if part.FormName() == "model" {
				data, _ := io.ReadAll(io.LimitReader(part, 4096))
				return string(data), true
			}
		}
	}
	var parsed struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.Model == "" {
		return "", false
	}
	return parsed.Model, true
}

// isSpecialVideoProvider names the media lane's special web
// providers whose video generations have dedicated handlers in
// forwardMediaRequest; none of them is a video provider in
// Axon's registry, so they never enter the core.
func isSpecialVideoProvider(provider string) bool {
	switch provider {
	case "uc-web", "uc", "veoaifree-web", "veo-free", "grok-web", "gw":
		return true
	}
	return false
}
