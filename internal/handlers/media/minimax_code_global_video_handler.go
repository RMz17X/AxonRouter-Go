package media

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlers/shared"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/models"
	"axonrouter/proxy/internal/providers"
)

const miniMaxVideoMaxAttempts = 2 // one core token refresh + one account rotation

// A balance refusal means the account never accepted the job, so it is safe
// to try another connection. The sweep stays small: enough to leave a spent
// account, small enough that one request cannot hammer the upstream from a
// single egress.
const miniMaxVideoBillingMaxAttempts = 8

// miniMaxVideoBalanceReserve is the credits one paid generation is expected
// to cost. Accounts below it are skipped while any account can cover it, so a
// sweep does not spend attempts on a refusal that is already predictable.
const miniMaxVideoBalanceReserve = 800

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

// miniMaxVideoCandidates keeps the richest accounts that can cover one paid
// generation. A snapshot below miniMaxVideoBalanceReserve is an account a
// single generate would exhaust or refuse; trying it is the expensive part of
// a sweep, so it is skipped while any sufficient account is known.
func miniMaxVideoCandidates(snaps []db.MiniMaxBalanceState, max int) []string {
	if max <= 0 {
		return nil
	}
	out := make([]string, 0, max)
	for _, s := range snaps {
		if s.Balance < miniMaxVideoBalanceReserve || s.ConnectionID == "" {
			continue
		}
		out = append(out, s.ConnectionID)
		if len(out) >= max {
			break
		}
	}
	return out
}

// miniMaxVideoBalanceMaxAge bounds how stale a credits snapshot may be before
// it stops ordering candidates. A snapshot older than this is ignored rather
// than trusted: credits move with every paid call.
const miniMaxVideoBalanceMaxAge = 24 * time.Hour

// miniMaxVideoCandidateOrder returns active connection ids richest-first,
// bounded by the same rotation cap as a billing sweep. An empty result means
// no fresh solvent snapshot exists, and the caller falls back to the normal
// strategy under the same small bound.
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
	return miniMaxVideoCandidates(snaps[:max], max)
}

// miniMaxVideoAttemptPreference picks the pin for one attempt. A client or
// model pin wins until it has answered a refusal: once it is in the exclude
// set, the balance-ordered candidate list takes over so a dead pinned account
// cannot hold the whole sweep on its own error. An empty result means the
// list is exhausted.
func miniMaxVideoAttemptPreference(pinned string, candidates []string, exclude []string) string {
	if pinned != "" && !slices.Contains(exclude, pinned) {
		return pinned
	}
	for _, candidate := range candidates {
		if !slices.Contains(exclude, candidate) {
			return candidate
		}
	}
	return ""
}

// refreshMiniMaxBalance re-reads the credits snapshot for a connection after
// a paid video or image request. Best-effort: a failed read leaves the previous
// snapshot in place.
func (h *MediaHandler) refreshMiniMaxBalance(ctx context.Context, conn *models.ProviderConnection, connData *shared.ConnectionData) {
	if h == nil || h.Repo == nil || h.MiniMaxBalance == nil || conn == nil || connData == nil {
		return
	}
	if conn.Provider != "minimax-code" && conn.Provider != "minimax-code-global" {
		return
	}
	userID, _ := connData.ProviderSpecificData["realUserID"].(string)
	balance, plan, ok := h.MiniMaxBalance(ctx, conn.Provider, connData.AccessToken, userID)
	if !ok {
		return
	}
	if err := h.Repo.UpdateMiniMaxBalance(conn.ID, conn.Provider, balance, plan); err != nil {
		log.Warn("media", "minimax balance snapshot not stored", "conn", conn.ID, "error", err)
	}
}

// noteMiniMaxImageOutcome keeps the credits snapshot in step with a paid
// image request: a 402 sinks the account, a success re-reads the balance.
func (h *MediaHandler) noteMiniMaxImageOutcome(ctx context.Context, status int, conn *models.ProviderConnection, connData *shared.ConnectionData) {
	if conn == nil || connData == nil {
		return
	}
	if conn.Provider != "minimax-code" && conn.Provider != "minimax-code-global" {
		return
	}
	if status == http.StatusPaymentRequired && h.Repo != nil {
		if err := h.Repo.UpdateMiniMaxBalance(conn.ID, conn.Provider, miniMaxBalanceInsufficient, ""); err != nil {
			log.Warn("media", "minimax insufficient balance mark failed", "conn", conn.ID, "error", err)
		}
		return
	}
	if status >= 200 && status < 300 {
		h.refreshMiniMaxBalance(ctx, conn, connData)
	}
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
		// A balance-ordered list is the whole sweep: once every known-solvent
		// account has answered, falling back to the normal strategy would
		// retry accounts the snapshot already marked empty.
		if len(candidates) > 0 && attempt >= len(candidates) {
			break
		}
		attemptPreferred := miniMaxVideoAttemptPreference(pinned, candidates, exclude)
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
				if err := h.rememberMiniMaxVideoTask(taskID, model, conn.ID); err != nil {
					log.Warn("media", "minimax task model not persisted", "task", taskID, "error", err)
				}
			}
			writeVideoResult(w, res, conn.ID)
			h.refreshMiniMaxBalance(r.Context(), conn, connData)
			return
		}
		log.Debug("media", "minimax video attempt refused", "attempt", attempt, "conn", conn.ID, "status", lastStatus, "error", lastError)
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
	meta, _ := h.recallMiniMaxVideoTask(id)
	queryModel := meta.Model
	if queryModel == "" {
		queryModel = miniMaxMatrixH3Model
	}
	queryBody, err := buildMiniMaxMatrixVideoQuery(queryModel, id)
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
