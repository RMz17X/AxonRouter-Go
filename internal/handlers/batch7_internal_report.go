package handlers

import (
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	json "encoding/json/v2"

	"github.com/google/uuid"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
)

// POST /api/internal/proxy/report (batch 7, AxonRouter parity): a worker
// proxy reports the outcome of a request it served; the gateway records
// the usage + request detail and folds the outcome into the connection's
// status. Ports src/lib/usageProxyOutcome.ts's applyProxyOutcomeReport
// and the route's token gate / error mapping.

// HandleInternalProxyReport serves POST /api/internal/proxy/report.
func HandleInternalProxyReport(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokens := getInternalProxyTokens(repo)
		if !hasValidInternalAuth(r, tokens.reportToken) {
			writeInternalError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			writeInternalError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		var payload any
		if err := json.Unmarshal(raw, &payload); err != nil {
			writeInternalError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		report, _ := payload.(map[string]any)
		if report == nil {
			report = map[string]any{}
		}

		if err := applyProxyOutcomeReport(repo, report); err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
				"ok": false, "error": "report_ingestion_failed", "message": err.Error(),
			})
			return
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// reportTokens is normalizeReportTokens' result.
type reportTokens struct {
	prompt     int
	completion int
	cached     int
	hasCached  bool
}

// jsNumberCoerce applies JS Number() to a JSON value: numbers pass
// through, strings are parsed (empty string is 0), booleans are 1/0,
// anything else is NaN (reported as not ok).
func jsNumberCoerce(v any) (float64, bool) {
	switch value := v.(type) {
	case float64:
		return value, true
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return 0, true
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	case bool:
		if value {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// firstCoercedNumber resolves a ?? chain of candidate fields and coerces
// the winner with Number() semantics (defaulting to 0).
func firstCoercedNumber(record map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if v, present := record[key]; present && v != nil {
			return jsNumberCoerce(v)
		}
	}
	return 0, true
}

// normalizeReportTokens ports normalizeReportTokens.
func normalizeReportTokens(report map[string]any) *reportTokens {
	usage, ok := report["usage"].(map[string]any)
	if !ok {
		return nil
	}
	prompt, okP := firstCoercedNumber(usage, "prompt_tokens", "input_tokens", "promptTokens", "inputTokens")
	completion, okC := firstCoercedNumber(usage, "completion_tokens", "output_tokens", "completionTokens", "outputTokens")
	cached, okK := firstCoercedNumber(usage, "cached_tokens", "cachedTokens", "cache_read_input_tokens")
	if !okP || !okC || !okK {
		return nil
	}
	if prompt <= 0 && completion <= 0 && cached <= 0 {
		return nil
	}
	tokens := &reportTokens{
		prompt:     max(0, int(math.Trunc(prompt))),
		completion: max(0, int(math.Trunc(completion))),
	}
	if cached > 0 {
		tokens.cached = max(0, int(math.Trunc(cached)))
		tokens.hasCached = true
	}
	return tokens
}

// mapReportStatus ports mapReportStatus.
func mapReportStatus(report map[string]any) string {
	outcome := strings.ToLower(recStrValue(report, "outcome"))
	if outcome == "error" || outcome == "failed" || outcome == "failure" {
		return "error"
	}
	if v, present := report["upstreamStatus"]; present && v != nil {
		if status, ok := jsNumberCoerce(v); ok && status >= 400 {
			return "error"
		}
	}
	if jsTruthy(report["error"]) {
		return "error"
	}
	return "ok"
}

// reportObservedAt ports getReportObservedAt.
func reportObservedAt(report map[string]any) string {
	for _, key := range []string{"observedAt", "finishedAt", "timestamp"} {
		if s := recStrValue(report, key); s != "" {
			return s
		}
	}
	return isoNowJS()
}

// reportStatusCode resolves the numeric upstream status (Number()
// coercion, non-finite becomes absent).
func reportStatusCode(report map[string]any) *float64 {
	v, present := report["upstreamStatus"]
	if !present || v == nil {
		return nil
	}
	status, ok := jsNumberCoerce(v)
	if !ok {
		return nil
	}
	return &status
}

// applyProxyOutcomeReport ports applyProxyOutcomeReport.
func applyProxyOutcomeReport(repo *db.Repo, report map[string]any) error {
	connectionID := recStrValue(report, "connectionId")
	provider := recStrValue(report, "provider")
	model := recStrValue(report, "model")
	if model == "" {
		model = recStrValue(report, "requestedModel")
	}
	observedAt := reportObservedAt(report)
	status := mapReportStatus(report)
	tokens := normalizeReportTokens(report)

	usageEvidence, hasUsage := report["usage"].(map[string]any)
	if !hasUsage {
		usageEvidence = nil
	}
	quotasEvidence, hasQuotas := report["quotas"].(map[string]any)
	if !hasQuotas {
		quotasEvidence = nil
	}
	hasCanonicalUsageEvidence := tokens != nil || usageEvidence != nil || quotasEvidence != nil

	if tokens != nil {
		if err := saveReportUsage(repo, report, provider, model, connectionID, status, tokens); err != nil {
			return err
		}
	}
	if err := saveReportDetail(repo, report, provider, model, connectionID, status, observedAt, tokens); err != nil {
		return err
	}

	if connectionID == "" {
		return nil
	}
	conn, err := repo.GetProviderConnectionByID(connectionID)
	if err != nil {
		return err
	}
	if conn == nil {
		return nil
	}
	flat := flattenProviderConnection(conn)

	statusCode := reportStatusCode(report)
	errorValue := report["error"]
	errorMessage := errorValue
	if message := errorMessageText(errorValue); message != "" {
		errorMessage = message
	}
	errorCode := errorCodeText(errorValue)

	if status == "error" {
		switch {
		case isTransientUpstreamTimeoutError(errorValue, statusCode, errorCode):
			syncUsageStatus(repo, flat, map[string]any{
				"lastCheckedAt": observedAt,
				"lastError":     nil,
				"lastErrorType": nil,
				"lastErrorAt":   nil,
				"errorCode":     nil,
			})
		case isUpstreamProcessingError(statusCode, errorMessage):
			syncUsageStatus(repo, flat, map[string]any{
				"lastCheckedAt": observedAt,
			})
		default:
			if authPatch := getConnectionAuthBlockedPatch(errorMessage, observedAt, statusCode); authPatch != nil {
				syncUsageStatus(repo, flat, authPatch)
				return nil
			}
			errorText, _ := errorMessage.(string)
			if signal := getCodexLiveQuotaSignal(flat, statusCode, errorText, errorCode); signal != nil {
				applyLiveQuotaUpdate(repo, flat, signal, observedAt)
				return nil
			}
			lastError := errorMessage
			if !jsTruthy(lastError) {
				lastError = "Proxy request failed"
			}
			code := errorCode
			if code == "" {
				code = "proxy_error"
			}
			syncUsageStatus(repo, flat, map[string]any{
				"healthStatus":  "degraded",
				"lastCheckedAt": observedAt,
				"lastError":     lastError,
				"lastErrorType": "proxy_error",
				"lastErrorAt":   observedAt,
				"errorCode":     code,
			})
		}
		return nil
	}

	if hasCanonicalUsageEvidence {
		applyCanonicalUsageRefresh(repo, flat, map[string]any{
			"quotas": quotasEvidence,
			"usage":  usageEvidence,
		}, observedAt)
		return nil
	}

	syncUsageStatus(repo, flat, map[string]any{
		"lastCheckedAt": observedAt,
		"usageSnapshot": string(mustJSON(usageEvidence)),
	})
	return nil
}

// applyCanonicalUsageRefresh ports applyCanonicalUsageRefresh.
func applyCanonicalUsageRefresh(repo *db.Repo, flat map[string]any, usage map[string]any, observedAt string) {
	updates := getUsageStatusUpdates(flat, usage, usageStatusOptions{observedAt: observedAt})
	syncUsageStatus(repo, flat, updates)
}

// applyLiveQuotaUpdate ports applyLiveQuotaUpdate.
func applyLiveQuotaUpdate(repo *db.Repo, flat map[string]any, signal *codexLiveQuotaSignal, observedAt string) {
	if signal == nil {
		return
	}
	updates := getUsageStatusUpdates(flat, nil, usageStatusOptions{observedAt: observedAt, liveSignal: signal})
	syncUsageStatus(repo, flat, updates)
}

// saveReportUsage records the usage row (upstream's saveRequestUsage).
// The Go usage writer stamps arrival time and takes a precomputed cost;
// upstream's pipeline prices downstream, so cost is recorded as 0 and
// the reported observation time lives on the request detail instead
// (see .changes/918). The meta argument is an empty JSON object, not
// an empty string: usageHistory.meta is JSONB, and PostgreSQL rejects
// "" as invalid JSON (SQLSTATE 22P02) — an empty string here failed
// every usage-carrying report (council round 2, R2-N1).
func saveReportUsage(repo *db.Repo, report map[string]any, provider, model, connectionID, status string, tokens *reportTokens) error {
	tokensMap := map[string]any{
		"prompt_tokens":     tokens.prompt,
		"completion_tokens": tokens.completion,
	}
	if tokens.hasCached {
		tokensMap["cached_tokens"] = tokens.cached
	}
	endpoint := recStrValue(report, "publicPath")
	if endpoint == "" {
		endpoint = recStrValue(report, "route")
	}
	return repo.InsertUsageHistory(
		provider, model, connectionID, "", endpoint,
		tokens.prompt, tokens.completion, 0, status,
		tokens.prompt+tokens.completion, "{}", string(mustJSON(tokensMap)),
	)
}

// saveReportDetail records the request detail (upstream's
// saveRequestDetail): the full detail object becomes the row's data
// payload, with the reported timestamp preserved inside it.
func saveReportDetail(repo *db.Repo, report map[string]any, provider, model, connectionID, status, observedAt string, tokens *reportTokens) error {
	id := recStrValue(report, "requestId")
	if id == "" {
		id = recStrValue(report, "id")
	}
	if id == "" {
		id = uuid.New().String()
	}
	tokensMap := map[string]any{}
	if tokens != nil {
		tokensMap["prompt_tokens"] = tokens.prompt
		tokensMap["completion_tokens"] = tokens.completion
		if tokens.hasCached {
			tokensMap["cached_tokens"] = tokens.cached
		}
	}
	detail := map[string]any{
		"id":           id,
		"provider":     nilOrString(provider),
		"model":        nilOrString(model),
		"connectionId": nilOrString(connectionID),
		"timestamp":    observedAt,
		"status":       status,
		"latency":      map[string]any{"totalMs": report["latencyMs"]},
		"tokens":       tokensMap,
		"request": map[string]any{
			"protocolFamily": report["protocolFamily"],
			"publicPath":     report["publicPath"],
			"method":         report["method"],
		},
		"providerRequest": map[string]any{
			"requestId": report["requestId"],
		},
		"providerResponse": map[string]any{
			"status": report["upstreamStatus"],
			"error":  report["error"],
		},
		"response": map[string]any{
			"outcome": report["outcome"],
		},
	}
	return repo.InsertRequestDetail(id, provider, model, connectionID, status, string(mustJSON(detail)))
}

// nilOrString maps "" to nil (upstream's `value || null` for strings).
func nilOrString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
