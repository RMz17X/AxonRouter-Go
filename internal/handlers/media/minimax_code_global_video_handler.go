package media

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"axonrouter/proxy/internal/handlers/shared"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/models"
	"axonrouter/proxy/internal/providers"
)

const miniMaxVideoMaxAttempts = 2 // one core token refresh + one account rotation

func (h *MediaHandler) handleMiniMaxVideoCreate(w http.ResponseWriter, r *http.Request, body []byte, action, provider, model, preferred string) {
	if action != "generations" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest,
			fmt.Sprintf("[%s] MiniMax video supports 'generations' only (got '%s')", provider, action))
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "application/json") {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "[minimax-code-global] MiniMax video requires an application/json body")
		return
	}
	finalBody, err := validateMiniMaxVideoBody(body, model)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "[minimax-code-global] "+err.Error())
		return
	}

	var exclude []string
	lastStatus, lastError := 0, ""
	for attempt := 0; attempt < miniMaxVideoMaxAttempts; attempt++ {
		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), provider, preferred, exclude, model)
		if err != nil || conn == nil || connData == nil {
			if attempt == 0 {
				handlerutil.WriteJSONError(w, http.StatusServiceUnavailable, "No credentials for provider: "+provider)
				return
			}
			break
		}
		cfg := h.videoRegistryConfig(provider, connData)
		target, err := miniMaxVideoCreateURL(cfg, connData)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, "[minimax-code-global] "+err.Error())
			return
		}
		start := time.Now()
		res := h.videoProxy(r.Context(), provider, cfg, conn, connData, videoProxyParams{
			Action:         action,
			Body:           finalBody,
			ContentType:    "application/json",
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
			TargetOverride: target,
		})
		h.logVideoUsage(r, provider, model, conn, connData, time.Since(start))
		if status, msg, ok := miniMaxMatrixBizError(res.Body); ok {
			writeVideoUpstreamError(w, &videoUpstreamResponse{Status: status,
				ErrText: fmt.Sprintf("[%s] %s", provider, msg)})
			return
		}
		if res.ErrText == "" {
			h.Repo.UpdateConnectionLastUsed(conn.ID)
			if taskID := miniMaxMatrixTaskID(res.Body); taskID != "" {
				if err := h.rememberMiniMaxVideoTask(taskID, model); err != nil {
					log.Warn("media", "minimax task model not persisted", "task", taskID, "error", err)
				}
			}
			writeVideoResult(w, res, conn.ID)
			return
		}
		lastStatus, lastError = res.Status, res.ErrText
		// The core already refreshed the token once. Rotate the account once
		// on auth failure, never on 429 or a server-side error.
		if attempt+1 < miniMaxVideoMaxAttempts &&
			(res.Status == http.StatusUnauthorized || res.Status == http.StatusForbidden) {
			exclude = append(exclude, conn.ID)
			continue
		}
		writeVideoUpstreamError(w, res)
		return
	}
	writeVideoFinalError(w, lastStatus, lastError)
}

func (h *MediaHandler) handleMiniMaxVideoGet(w http.ResponseWriter, r *http.Request, provider string,
	conn *models.ProviderConnection, connData *shared.ConnectionData, cfg providers.ProviderConfig, id string, content bool) {
	target, err := miniMaxVideoQueryURL(cfg, connData, id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "[minimax-code-global] "+err.Error())
		return
	}
	queryBody, err := buildMiniMaxMatrixVideoQuery(h.recallMiniMaxVideoModel(id), id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "[minimax-code-global] "+err.Error())
		return
	}
	start := time.Now()
	res := h.videoProxy(r.Context(), provider, cfg, conn, connData, videoProxyParams{
		RequestID: id, QueryBody: queryBody, ContentType: "application/json", TargetOverride: target,
	})
	h.logVideoUsage(r, provider, "video", conn, connData, time.Since(start))
	if status, msg, ok := miniMaxMatrixBizError(res.Body); ok {
		writeVideoUpstreamError(w, &videoUpstreamResponse{Status: status,
			ErrText: fmt.Sprintf("[%s] %s", provider, msg), RetryAfter: "10"})
		return
	}
	if res.ErrText != "" {
		writeVideoUpstreamError(w, res)
		return
	}
	h.Repo.UpdateConnectionLastUsed(conn.ID)
	if !content {
		writeVideoResult(w, res, conn.ID)
		return
	}

	task, err := parseMiniMaxVideoPoll(res.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "[minimax-code-global] "+err.Error())
		return
	}
	if !task.terminal() {
		w.Header().Set("Retry-After", fmt.Sprint(miniMaxVideoPollSeconds))
		handlerutil.WriteJSONError(w, http.StatusConflict, "video task is still running")
		return
	}
	if task.Status != "succeeded" && !strings.EqualFold(task.Status, "succeeded") {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "[minimax-code-global] "+task.errorText())
		return
	}
	contentURL, err := miniMaxVideoContentURL(task.contentURL())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "[minimax-code-global] "+err.Error())
		return
	}
	client, err := h.ChatH.GetClientForConnection(connData)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "[minimax-code-global] "+err.Error())
		return
	}
	token := connData.AccessToken
	if token == "" {
		token = connData.APIKey
	}
	if token == "" {
		token = cfg.DefaultAPIKey
	}
	download := h.videoFetch(r.Context(), client, provider, contentURL, token, cfg, videoProxyParams{
		RequestID: id, Content: true, SkipAuth: true,
	})
	if download.ErrText != "" {
		writeVideoUpstreamError(w, download)
		return
	}
	writeVideoResult(w, download, conn.ID)
}

func writeVideoFinalError(w http.ResponseWriter, status int, msg string) {
	if status == 0 {
		status = http.StatusServiceUnavailable
	}
	if msg == "" {
		msg = "All accounts unavailable"
	}
	handlerutil.WriteJSONError(w, status, msg)
}
