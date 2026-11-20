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

// A balance refusal means the account never accepted the job, so it is safe
// to walk the whole connection list rather than stopping after a handful:
// the upstream only answers 402 when the account cannot pay, so the next
// account is a fresh chance. Bounded so a request can never loop forever.
const miniMaxVideoBillingMaxAttempts = 128

// miniMaxBalanceInsufficient marks a connection that answered a paid-video
// 402. It is not a real balance, but it sinks the account below every known
// balance so the next create tries the accounts that can still pay first.
const miniMaxBalanceInsufficient = -1

// miniMaxVideoShouldRotate reports whether a refusal is account-scoped and
// safe to retry on another connection. A 5xx is absent on purpose: the job may
// already exist upstream and a replay could bill it twice.
func miniMaxVideoShouldRotate(status int) bool {
	return status == http.StatusUnauthorized ||
		status == http.StatusForbidden ||
		status == http.StatusPaymentRequired
}

// miniMaxVideoAttemptLimit bounds the loop for the refusal class that just
// answered. Billing refusals get the wider cap; everything else keeps the
// original single rotation.
func miniMaxVideoAttemptLimit(lastStatus int) int {
	if lastStatus == http.StatusPaymentRequired {
		return miniMaxVideoBillingMaxAttempts
	}
	return miniMaxVideoMaxAttempts
}

// miniMaxVideoBalanceMaxAge bounds how stale a credits snapshot may be before
// it stops ordering candidates. A snapshot older than this is ignored rather
// than trusted: credits move with every paid call.
const miniMaxVideoBalanceMaxAge = 24 * time.Hour

// miniMaxVideoCandidateOrder returns active connection ids richest-first,
// bounded by the same rotation cap as a billing sweep. An empty result means
// no fresh snapshot exists, and the caller falls back to the normal strategy.
func (h *MediaHandler) miniMaxVideoCandidateOrder(provider string) []string {
	if h == nil || h.Repo == nil {
		return nil
	}
	snaps, err := h.Repo.MiniMaxBalanceSnapshots(provider, miniMaxVideoBalanceMaxAge)
	if err != nil || len(snaps) == 0 {
		return nil
	}
	max := miniMaxVideoBillingMaxAttempts
	if len(snaps) < max {
		max = len(snaps)
	}
	out := make([]string, 0, max)
	for _, s := range snaps[:max] {
		if s.ConnectionID != "" {
			out = append(out, s.ConnectionID)
		}
	}
	return out
}

// miniMaxVideoAttemptPreference picks the pin for one attempt. A client or
// model pin always wins; otherwise the balance-ordered candidate for this
// attempt is tried first, and once the list is exhausted the normal strategy
// resumes.
func miniMaxVideoAttemptPreference(attempt int, pinned string, candidates []string) string {
	if pinned != "" {
		return pinned
	}
	if attempt >= 0 && attempt < len(candidates) {
		return candidates[attempt]
	}
	return ""
}

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
	pinned := preferred
	var candidates []string
	if pinned == "" {
		candidates = h.miniMaxVideoCandidateOrder(provider)
	}

	var exclude []string
	lastStatus, lastError := 0, ""
	for attempt := 0; ; attempt++ {
		if attempt >= miniMaxVideoAttemptLimit(lastStatus) {
			break
		}
		attemptPreferred := miniMaxVideoAttemptPreference(attempt, pinned, candidates)
		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), provider, attemptPreferred, exclude, model)
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
			lastStatus, lastError = status, fmt.Sprintf("[%s] %s", provider, msg)
		} else if res.ErrText != "" {
			lastStatus, lastError = res.Status, res.ErrText
		} else {
			h.Repo.UpdateConnectionLastUsed(conn.ID)
			if taskID := miniMaxMatrixTaskID(res.Body); taskID != "" {
				if err := h.rememberMiniMaxVideoTask(taskID, model); err != nil {
					log.Warn("media", "minimax task model not persisted", "task", taskID, "error", err)
				}
			}
			writeVideoResult(w, res, conn.ID)
			return
		}
		// A 402 means the account never accepted the job, so it is safe to
		// walk on; 401/403 keep the original single rotation. A 5xx is never
		// replayed: the job may already exist upstream.
		if miniMaxVideoShouldRotate(lastStatus) {
			if lastStatus == http.StatusPaymentRequired && h.Repo != nil {
				// The account cannot cover this paid task. Sink it in the
				// snapshot ranking so the next create tries the accounts that
				// can still pay first; a dashboard usage read overwrites it.
				if err := h.Repo.UpdateMiniMaxBalance(conn.ID, provider, miniMaxBalanceInsufficient, ""); err != nil {
					log.Warn("media", "minimax insufficient balance mark failed", "conn", conn.ID, "error", err)
				}
			}
			exclude = append(exclude, conn.ID)
			continue
		}
		writeVideoUpstreamError(w, &videoUpstreamResponse{
			Status: lastStatus, ErrText: lastError, RetryAfter: res.RetryAfter,
		})
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
