package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"axonrouter/proxy/internal/constants"
	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/detection"
	"axonrouter/proxy/internal/handlers/shared"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/middleware"
	"axonrouter/proxy/internal/observ"
	"axonrouter/proxy/internal/pricing"
	"axonrouter/proxy/internal/translator"
	"axonrouter/proxy/internal/usagetracker"
)

// usageReqSeq makes usage request IDs unique within the process; see
// newUsageRequestID.
var usageReqSeq atomic.Uint64

// newUsageRequestID mints the ID a request's usageHistory and
// requestDetails rows share. It used to be just "<unixMilli>-<model>":
// under any real concurrency, requests landing in the same millisecond
// with the same model minted the SAME id, and the detail insert's
// ON CONFLICT DO NOTHING silently ate the duplicates — a ledger-loss
// channel independent of the writer queue (found by the review-cycle-1
// F2 probe: with the queue fixed, history reconciled exactly while
// details still landed ~19% short at 100 workers). The atomic sequence
// guarantees uniqueness inside the process; the random tail covers two
// cluster nodes sharing both the millisecond and the sequence value.
func newUsageRequestID(now time.Time, model string) string {
	seq := usageReqSeq.Add(1)
	var tail [4]byte
	if _, err := rand.Read(tail[:]); err != nil {
		return fmt.Sprintf("%d-%s-%d", now.UnixMilli(), model, seq)
	}
	return fmt.Sprintf("%d-%s-%d-%s", now.UnixMilli(), model, seq, hex.EncodeToString(tail[:]))
}

// shouldLogRequestDetail checks if request detail logging is enabled by settings.
func shouldLogRequestDetail(repo *db.Repo, isError bool) bool {
	if repo == nil {
		return true
	}
	settings, err := repo.GetSettings()
	if err != nil || settings == nil {
		return true
	}
	if settings.ObservabilityMode == "off" {
		return false
	}
	if settings.ObservabilityMode == "errors_only" && !isError {
		return false
	}
	return true
}

var dailyUsageMu sync.Mutex

const maxPersistedErrorLen = 512

// LogFailure persists a bounded, credential-free diagnostic for a failed or
// aborted request. It intentionally does not touch usageHistory/daily totals.
//
// ctx is the request context, and it is not optional: the abort verdict is
// taken from it, so a call site that passes a nil context records a client
// abort as an ordinary 502 while the response says 499. The same context and
// the same classifier decide both, so the row and the response agree.
// callerDetailFields returns the request-detail payload fields naming the
// gateway caller: the client API key's name and masked form, and the
// client IP the entry points resolved. The payload's own "apiKey" field
// is the UPSTREAM credential the request used and stays untouched — these
// fields are the key the CLIENT presented, which is what an operator
// auditing "who called, from where" is asking about. Absent pieces stay
// absent (a keyless caller has no name to record), never zero-filled.
func callerDetailFields(ctx context.Context) map[string]any {
	fields := map[string]any{}
	if caller := middleware.AuthenticatedApiKeyFromContext(ctx); caller != nil {
		if caller.Name != nil && strings.TrimSpace(*caller.Name) != "" {
			fields["apiKeyName"] = *caller.Name
		}
		fields["clientApiKey"] = maskAPIKey(caller.Key)
	}
	if ip := clientIPFromContext(ctx); ip != "" {
		fields["clientIp"] = ip
	}
	return fields
}

// requestIDDetailField returns the detail-payload field carrying the
// middleware request ID (req_<hex12>) — the same value every console log
// line of this request is stamped with, so a stored request detail and its
// log trail join on one exact string. Absent when the context carries no
// ID, never zero-filled, like callerDetailFields above.
func requestIDDetailField(ctx context.Context) map[string]any {
	if rid := middleware.GetRequestIDFromContext(ctx); rid != "" {
		return map[string]any{"reqId": rid}
	}
	return nil
}

// smartDecisionDetailField returns the detail-payload field carrying the
// smart router's decision for this request (see smart_router.go): the
// tier the AI led with, or the fallback verdict when it did not decide.
// Absent on non-smart requests and single-model combos, never
// zero-filled, like requestIDDetailField above.
func smartDecisionDetailField(ctx context.Context) map[string]any {
	if d := smartDecisionFrom(ctx); d != nil {
		return map[string]any{"smartDecision": d}
	}
	return nil
}

// dailyErrorFoldSeen remembers which requests already folded a daily
// error delta in this process, and under which provider/model/day, so a
// request that later SUCCEEDS can retract the fold (see LogUsage).
// LogFailure runs once per failed ATTEMPT, but the daily aggregate is
// read as requests: without the guard, a request that tried five
// accounts folded five errors, and a request that failed twice before
// succeeding counted as both a request and an error next to its success
// fold. The first failure of a request folds; its later attempts do
// not. Entries expire once the request is long finished, and the map
// is swept lazily when it grows past a size cap.
type dailyErrorFoldRecord struct {
	at       time.Time
	dateKey  string
	provider string
	model    string
}

var dailyErrorFoldSeen struct {
	mu   sync.Mutex
	seen map[string]dailyErrorFoldRecord
}

const dailyErrorFoldTTL = 15 * time.Minute

func firstDailyErrorFold(reqID, provider, model string) bool {
	if reqID == "" {
		// No request identity to dedupe on: fold as before.
		return true
	}
	now := time.Now()
	dailyErrorFoldSeen.mu.Lock()
	defer dailyErrorFoldSeen.mu.Unlock()
	if dailyErrorFoldSeen.seen == nil {
		dailyErrorFoldSeen.seen = map[string]dailyErrorFoldRecord{}
	}
	if len(dailyErrorFoldSeen.seen) > 8192 {
		for k, rec := range dailyErrorFoldSeen.seen {
			if now.Sub(rec.at) > dailyErrorFoldTTL {
				delete(dailyErrorFoldSeen.seen, k)
			}
		}
	}
	if rec, ok := dailyErrorFoldSeen.seen[reqID]; ok && now.Sub(rec.at) < dailyErrorFoldTTL {
		return false
	}
	dailyErrorFoldSeen.seen[reqID] = dailyErrorFoldRecord{
		at:       now,
		dateKey:  now.UTC().Format("2006-01-02"),
		provider: provider,
		model:    model,
	}
	return true
}

// takeDailyErrorFold removes and returns the error fold a request made,
// if any. LogUsage uses it to retract the fold of a request that went on
// to succeed.
func takeDailyErrorFold(reqID string) (dailyErrorFoldRecord, bool) {
	if reqID == "" {
		return dailyErrorFoldRecord{}, false
	}
	dailyErrorFoldSeen.mu.Lock()
	defer dailyErrorFoldSeen.mu.Unlock()
	rec, ok := dailyErrorFoldSeen.seen[reqID]
	if ok {
		delete(dailyErrorFoldSeen.seen, reqID)
	}
	return rec, ok
}

func (h *ChatHandler) LogFailure(
	ctx context.Context,
	info *UsageLogInfo,
	usage *translator.OpenAIUsage,
	err error,
	latencyMs int64,
	requestBody []byte,
	metrics *streamMetrics,
) {
	if info == nil || h.Repo == nil || err == nil {
		return
	}
	if usage == nil {
		usage = &translator.OpenAIUsage{}
	}
	now := time.Now().UTC()
	reqID := newUsageRequestID(now, info.Model)
	tokens := map[string]int{
		"prompt_tokens":               usage.PromptTokens,
		"completion_tokens":           usage.CompletionTokens,
		"cached_tokens":               usage.GetCachedTokens(),
		"cache_creation_input_tokens": usage.CacheCreationInputTokens,
	}
	if info.SavedTokens > 0 {
		tokens["original_input_tokens"] = info.OriginalInputTokens
		tokens["compressed_input_tokens"] = info.OriginalInputTokens - info.SavedTokens
		tokens["saved_tokens"] = info.SavedTokens
		tokens["saved_percent"] = info.SavedPercent
	}
	statusCode := http.StatusBadGateway
	var upstreamErr *upstreamError
	if errors.As(err, &upstreamErr) && upstreamErr.StatusCode > 0 {
		statusCode = upstreamErr.StatusCode
	}
	if isClientCanceled(ctx, err) {
		statusCode = StatusClientClosedRequest
	}
	message := ""
	if errors.As(err, &upstreamErr) && len(upstreamErr.Body) > 0 {
		message = extractErrorText(upstreamErr.Body)
		if message == "" {
			message = strings.TrimSpace(string(upstreamErr.Body))
		}
	}
	if message == "" && err != nil {
		message = extractErrorText([]byte(err.Error()))
	}
	if message == "" && err != nil {
		message = err.Error()
	}
	if message == "" {
		message = "request failed"
	}
	message = sanitizeDetailError(message)
	var responseContent string
	if metrics != nil {
		responseContent = metrics.ResponseBuf.String()
		if len(responseContent) > constants.MaxResponseContentLen {
			responseContent = truncateDetailText(responseContent, constants.MaxResponseContentLen, "...[truncated]")
		}
	}
	startedAt := info.StartedAt
	if startedAt.IsZero() {
		startedAt = now.Add(-time.Duration(latencyMs) * time.Millisecond)
	}

	status := "error"
	if isClientCanceled(ctx, err) || statusCode == StatusClientClosedRequest {
		status = "canceled"
	}

	var openaiReq translator.OpenAIRequest
	_ = json.Unmarshal(requestBody, &openaiReq)
	tools := detection.ExtractToolCallsFromOpenAIMessages(openaiReq.Messages)
	findings := detection.DetectLoops(tools, message)
	var loopWarning string

	failurePayload := map[string]any{
		"id":             reqID,
		"provider":       info.Provider,
		"model":          info.Model,
		"requestedModel": info.RequestedModel,
		"connectionId":   info.ConnectionID,
		"connName":       info.ConnName,
		"connEmail":      info.ConnEmail,
		"account":        info.AccountLabel(),
		"apiKey":         maskAPIKey(info.APIKey),
		"combo":          info.ComboName,
		"protocol":       info.Protocol,
		"isStream":       info.IsStream,
		"cacheSource":    info.CacheSource,
		"status":         status,
		"timestamp":      now.Format("2006-01-02T15:04:05.000Z"),
		"startedAt":      startedAt.Format("2006-01-02T15:04:05.000Z"),
		"endedAt":        now.Format("2006-01-02T15:04:05.000Z"),
		"latency": map[string]int64{
			"ttft":  metricsTTFT(metrics),
			"total": latencyMs,
		},
		"tokens":   tokens,
		"request":  map[string]any{"messages": extractMessagesForLogging(openaiReq.Messages)},
		"response": map[string]any{"error": message, "status": statusCode, "content": responseContent},
		"proxy":    info.Egress,
		"egress":   info.Egress,
	}
	if len(findings) > 0 {
		failurePayload["anomalies"] = findings
		loopWarning = findings[0].Message
	}

	if rec := FallbackAttemptRecorderFrom(ctx); rec != nil {
		if attempts := rec.Snapshot(); len(attempts) > 0 {
			failurePayload["attempts"] = attempts
		}
	}
	for k, v := range callerDetailFields(ctx) {
		failurePayload[k] = v
	}
	for k, v := range requestIDDetailField(ctx) {
		failurePayload[k] = v
	}
	for k, v := range smartDecisionDetailField(ctx) {
		failurePayload[k] = v
	}
	reqData, marshalErr := json.Marshal(failurePayload)
	if marshalErr != nil {
		return
	}

	isProxyBlocked := upstreamErr != nil && strings.Contains(string(upstreamErr.Body), `"proxy_error"`)

	failureRepo := h.Repo
	failureID, failureProvider := reqID, info.Provider
	failureModel, failureConn := info.Model, info.ConnectionID
	failureData := string(reqData)
	failureAPIKey, failureEndpoint := info.APIKey, info.Endpoint

	logDetail := shouldLogRequestDetail(h.Repo, true)

	// The failure detail row is ledger: it rides the never-droppable
	// ledger lane, not the generic queue (review F2). The daily error
	// fold accumulates like the success fold does.
	if logDetail {
		enqueueLedgerWrite(ledgerWrite{
			repo: failureRepo,
			detail: &db.RequestDetailRecord{
				ID:           failureID,
				Timestamp:    time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
				Provider:     failureProvider,
				Model:        failureModel,
				ConnectionID: failureConn,
				Status:       "error",
				Data:         failureData,
			},
		})
	}
	if !isProxyBlocked && status != "canceled" && firstDailyErrorFold(middleware.GetRequestIDFromContext(ctx), failureProvider, failureModel) {
		accumulateDailyUsage(failureRepo, dailyUsageDelta{
			provider:     failureProvider,
			model:        failureModel,
			endpoint:     failureEndpoint,
			connectionID: failureConn,
			apiKey:       failureAPIKey,
			failed:       true,
		})
	}

	usagetracker.GetTracker().PushRecent(usagetracker.RecentRequest{
		Timestamp:        now.Format(time.RFC3339),
		Model:            info.Model,
		Provider:         info.Provider,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		CachedTokens:     usage.GetCachedTokens(),
		Status:           status,
		RequestedModel:   info.RequestedModel,
		ComboName:        info.ComboName,
		IsStream:         info.IsStream,
		LatencyMs:        latencyMs,
		LoopWarning:      loopWarning,
		Endpoint:         info.Endpoint,
		APIKey:           maskAPIKey(info.APIKey),
		Account:          info.AccountLabel(),
		Proxy:            info.Egress,
		Egress:           info.Egress,
	}, h.Repo)

	observ.RecordRequest(info.Provider, info.Model, info.Endpoint, statusCode)
	observ.RecordDuration(info.Provider, info.Model, time.Duration(latencyMs)*time.Millisecond)
}

func metricsTTFT(metrics *streamMetrics) int64 {
	if metrics == nil {
		return 0
	}
	return metrics.TTFT
}

func sanitizeDetailError(message string) string {
	return truncateDetailText(message, maxPersistedErrorLen, "...[truncated]")
}

// truncateDetailText bounds text stored in a request-detail row.
//
// The cut is by byte, so it can land inside a multi-byte rune and leave a
// fragment that is no longer valid UTF-8. encoding/json/v2 — the marshaler
// used for these rows — rejects such a string outright instead of replacing
// the bad bytes as encoding/json v1 did, so one CJK or emoji character
// straddling the limit used to fail the whole marshal and silently drop the
// row, the daily aggregate and the recent-requests entry with it.
//
// Invalid bytes are scrubbed on the untruncated path too: a value that
// arrived already malformed is just as fatal to the marshal as one this
// function damaged. The ContainsFunc guard keeps that path free — measured
// at 0 allocs/op — by returning the original string when nothing needs
// fixing. The truncated path allocates once for the concatenated result,
// which is what the bare s[:max] + suffix did as well.
func truncateDetailText(s string, max int, suffix string) string {
	if len(s) > max {
		if !strings.ContainsFunc(s[:max], isInvalidRune) {
			return s[:max] + suffix
		}
		return strings.ToValidUTF8(s[:max], "") + suffix
	}
	if !strings.ContainsFunc(s, isInvalidRune) {
		return s
	}
	return strings.ToValidUTF8(s, "")
}

// isInvalidRune reports whether r is a byte sequence Go could not decode.
func isInvalidRune(r rune) bool { return r == utf8.RuneError }

// LogUsage is the exported method to persist a usage record and update connection metadata.
func (h *ChatHandler) LogUsage(ctx context.Context, info *UsageLogInfo, usage *translator.OpenAIUsage, latencyMs int64, requestBody []byte, metrics *streamMetrics) {
	h.logUsage(ctx, info, usage, latencyMs, requestBody, metrics)
}

// logUsage persists a usage record and updates connection metadata.
func (h *ChatHandler) logUsage(ctx context.Context, info *UsageLogInfo, usage *translator.OpenAIUsage, latencyMs int64, requestBody []byte, metrics *streamMetrics) {
	if usage == nil {
		usage = &translator.OpenAIUsage{}
	}

	var ttftMs int64
	var respContent string
	if metrics != nil {
		ttftMs = metrics.TTFT
		respContent = metrics.ResponseBuf.String()
		if len(respContent) > constants.MaxResponseContentLen {
			respContent = truncateDetailText(respContent, constants.MaxResponseContentLen, "...[truncated]")
		}
	}

	// Fallback token estimation if upstream emitted zero tokens
	if usage.PromptTokens == 0 && len(requestBody) > 0 {
		usage.PromptTokens = CountValueChars(requestBody) / 4
		if usage.PromptTokens == 0 {
			usage.PromptTokens = 1
		}
	}
	if usage.CompletionTokens == 0 && len(respContent) > 0 {
		usage.CompletionTokens = CountValueChars(respContent) / 4
		if usage.CompletionTokens == 0 {
			usage.CompletionTokens = 1
		}
	}

	totalTokens := usage.PromptTokens + usage.CompletionTokens
	cachedTokens := usage.GetCachedTokens()
	cacheCreationTokens := usage.CacheCreationInputTokens

	// prompt_tokens is cache-inclusive, so the cached and cache-creation counts
	// are handed over too: the formula takes them out of the input rate and
	// charges them at their own, cheaper rate. The provider matters because
	// gateways can price the same model differently.
	cost := pricing.EstimateCost(info.Provider, info.Model, pricing.TokenCounts{
		PromptTokens:        usage.PromptTokens,
		CompletionTokens:    usage.CompletionTokens,
		CachedTokens:        cachedTokens,
		CacheCreationTokens: cacheCreationTokens,
		ReasoningTokens:     usage.ReasoningTokens(),
	})
	metaJSON := fmt.Sprintf(`{"provider":"%s","model":"%s","connectionId":"%s"}`, info.Provider, info.Model, info.ConnectionID)

	usageKVs := []any{
		"provider", info.Provider, "model", info.Model,
		"prompt", usage.PromptTokens, "completion", usage.CompletionTokens, "cached", cachedTokens,
		"cache_creation", cacheCreationTokens, "ttft_ms", ttftMs, "latency_ms", latencyMs, "cost", cost,
	}
	if info.SavedTokens > 0 {
		usageKVs = append(usageKVs, "compressed", fmt.Sprintf("%d->%d (%d%% saved)", info.OriginalInputTokens, info.OriginalInputTokens-info.SavedTokens, info.SavedPercent))
	}
	log.InfoCtx(ctx, "usage", "logged", append(usageKVs, info.ConnIdentityKV()...)...)

	// Reconcile the limiter's TPM reservation against what the turn actually
	// cost. Without this the bucket is charged an estimate forever: a key whose
	// prompts are consistently under-estimated spends past the limit that
	// exists to bound exactly that.
	//
	// The reservation was taken before dispatch, from a body the gateway may
	// since have rewritten, so the real total is the only honest number to
	// settle against.
	middleware.SettleTokenUsageFromContext(ctx, totalTokens)

	// reasoning_tokens belongs here for the same reason as the cache figures:
	// the stored row is what the dashboard bills and charts from, so omitting
	// it makes every reasoning turn look cheaper than it was.
	tokensJSON := fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,"cached_tokens":%d,"cache_creation_input_tokens":%d,"reasoning_tokens":%d}`, usage.PromptTokens, usage.CompletionTokens, totalTokens, cachedTokens, cacheCreationTokens, usage.ReasoningTokens())
	// The caller is recorded beside the upstream credential (#365). A per-key
	// token budget is measured from what THAT client spent, and
	// usageHistory.apiKey holds the provider credential — which attributed every
	// request to a backend account and left the budget reading zero forever.
	clientKey := ""
	if caller := middleware.AuthenticatedApiKeyFromContext(ctx); caller != nil {
		clientKey = maskAPIKey(caller.Key)
	}
	// The persistence units below leave the response goroutine: the
	// ledger rows ride the usage writer's ledger lane (batched, never
	// droppable), the daily aggregate accumulates in memory. Everything
	// the client or the limiter can observe (metrics, TPM settlement,
	// the recent-requests ring) stays synchronous.
	enqueueLedgerWrite(ledgerWrite{
		repo: h.Repo,
		history: &db.UsageHistoryRecord{
			Timestamp:        time.Now().UTC().Format(time.RFC3339),
			Provider:         info.Provider,
			Model:            info.Model,
			ConnectionID:     info.ConnectionID,
			APIKey:           maskAPIKey(info.APIKey),
			ClientKey:        clientKey,
			Endpoint:         info.Endpoint,
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			Cost:             cost,
			Status:           "success",
			Tokens:           tokensJSON,
			Meta:             metaJSON,
		},
	})

	// Prometheus mirrors the row that was just inserted: a request is counted
	// once, labelled with the provider/model the usage row names, and never
	// with the caller's API key.
	observ.RecordUsage(
		info.Provider, info.Model, info.Endpoint, http.StatusOK,
		time.Duration(latencyMs)*time.Millisecond,
		usage.PromptTokens, usage.CompletionTokens,
		cost*1_000_000, ttftMs,
	)

	now := time.Now().UTC()
	reqID := newUsageRequestID(now, info.Model)

	if h.Repo != nil && (info.SavedTokens > 0 || info.OriginalInputTokens > 0) {
		mode := resolveCompressionMode(h.TokenSaver, info.SavedTokens)
		skipReason := ""
		if info.SavedTokens == 0 {
			skipReason = "no_savings"
		}
		compressionRec := db.CompressionAnalyticsRecord{
			Timestamp:              now.Format(time.RFC3339),
			Provider:               info.Provider,
			Model:                  info.Model,
			Mode:                   mode,
			OriginalTokens:         info.OriginalInputTokens,
			CompressedTokens:       info.OriginalInputTokens - info.SavedTokens,
			TokensSaved:            info.SavedTokens,
			DurationMs:             int(latencyMs),
			RequestID:              reqID,
			ActualPromptTokens:     usage.PromptTokens,
			ActualCompletionTokens: usage.CompletionTokens,
			ActualTotalTokens:      totalTokens,
			ActualCacheReadTokens:  cachedTokens,
			ActualCacheWriteTokens: cacheCreationTokens,
			SkipReason:             skipReason,
		}
		compressionRepo := h.Repo
		enqueueUsageWrite(func() {
			_ = compressionRepo.InsertCompressionAnalytics(context.Background(), compressionRec)
		})
	}
	var openaiReq translator.OpenAIRequest
	_ = json.Unmarshal(requestBody, &openaiReq)
	tools := detection.ExtractToolCallsFromOpenAIMessages(openaiReq.Messages)
	findings := detection.DetectLoops(tools, "")
	var loopWarning string
	reqMsgs := extractMessagesForLogging(openaiReq.Messages)

	tokensMap := map[string]int{
		"prompt_tokens":               usage.PromptTokens,
		"completion_tokens":           usage.CompletionTokens,
		"cached_tokens":               cachedTokens,
		"cache_creation_input_tokens": cacheCreationTokens,
		"reasoning_tokens":            usage.ReasoningTokens(),
	}
	if info.SavedTokens > 0 {
		tokensMap["original_input_tokens"] = info.OriginalInputTokens
		tokensMap["compressed_input_tokens"] = info.OriginalInputTokens - info.SavedTokens
		tokensMap["saved_tokens"] = info.SavedTokens
		tokensMap["saved_percent"] = info.SavedPercent
	}

	startedAtUsage := info.StartedAt
	if startedAtUsage.IsZero() {
		startedAtUsage = now.Add(-time.Duration(latencyMs) * time.Millisecond)
	}

	detailPayload := map[string]any{
		"id":             reqID,
		"provider":       info.Provider,
		"model":          info.Model,
		"requestedModel": info.RequestedModel,
		"connectionId":   info.ConnectionID,
		"connName":       info.ConnName,
		"connEmail":      info.ConnEmail,
		"account":        info.AccountLabel(),
		"apiKey":         maskAPIKey(info.APIKey),
		"combo":          info.ComboName,
		"protocol":       info.Protocol,
		"isStream":       info.IsStream,
		"cacheSource":    info.CacheSource,
		"status":         "success",
		"timestamp":      now.Format("2006-01-02T15:04:05.000Z"),
		"startedAt":      startedAtUsage.Format("2006-01-02T15:04:05.000Z"),
		"endedAt":        now.Format("2006-01-02T15:04:05.000Z"),
		"latency":        map[string]int64{"ttft": ttftMs, "total": latencyMs},
		"tokens":         tokensMap,
		"cost":           cost,
		"request":        map[string]any{"messages": reqMsgs},
		"response":       map[string]any{"content": respContent},
		"proxy":          info.Egress,
		"egress":         info.Egress,
	}
	if len(findings) > 0 {
		detailPayload["anomalies"] = findings
		loopWarning = findings[0].Message
	}
	if rec := FallbackAttemptRecorderFrom(ctx); rec != nil {
		if attempts := rec.Snapshot(); len(attempts) > 0 {
			detailPayload["attempts"] = attempts
		}
	}
	for k, v := range callerDetailFields(ctx) {
		detailPayload[k] = v
	}
	for k, v := range requestIDDetailField(ctx) {
		detailPayload[k] = v
	}
	for k, v := range smartDecisionDetailField(ctx) {
		detailPayload[k] = v
	}
	reqData, err := json.Marshal(detailPayload)
	if err != nil {
		log.ErrorCtx(ctx, "usage", "marshal request detail failed", "error", err)
		return
	}
	// The settings read decides the row's fate now; only the write leaves.
	if shouldLogRequestDetail(h.Repo, false) {
		enqueueLedgerWrite(ledgerWrite{
			repo: h.Repo,
			detail: &db.RequestDetailRecord{
				ID:           reqID,
				Timestamp:    time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
				Provider:     info.Provider,
				Model:        info.Model,
				ConnectionID: info.ConnectionID,
				Status:       "success",
				Data:         string(reqData),
			},
		})
	}

	accumulateDailyUsage(h.Repo, dailyUsageDelta{
		provider:     info.Provider,
		model:        info.Model,
		endpoint:     info.Endpoint,
		connectionID: info.ConnectionID,
		apiKey:       info.APIKey,
		promptTokens: usage.PromptTokens,
		completion:   usage.CompletionTokens,
		cachedTokens: cachedTokens,
		cost:         cost,
	})

	// A request that failed on earlier attempts already folded a daily
	// error delta (requests + errors); this success folds its own
	// request on top, so without a retraction the day counts the
	// request twice and keeps a phantom error. Retract the error fold
	// under its original provider/model when it landed on this same
	// day; a fold from a previous day stays, as that day's record of
	// the attempts stands.
	if rec, ok := takeDailyErrorFold(middleware.GetRequestIDFromContext(ctx)); ok && rec.dateKey == time.Now().UTC().Format("2006-01-02") {
		accumulateDailyUsage(h.Repo, dailyUsageDelta{
			dateKey:  rec.dateKey,
			provider: rec.provider,
			model:    rec.model,
			retract:  true,
		})
	}

	usagetracker.GetTracker().PushRecent(usagetracker.RecentRequest{
		Timestamp:        now.Format(time.RFC3339),
		Model:            info.Model,
		Provider:         info.Provider,
		RequestedModel:   info.RequestedModel,
		ComboName:        info.ComboName,
		IsStream:         info.IsStream,
		LatencyMs:        latencyMs,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		CachedTokens:     cachedTokens,
		Status:           "ok",
		LoopWarning:      loopWarning,
		Endpoint:         info.Endpoint,
		APIKey:           maskAPIKey(info.APIKey),
		Account:          info.AccountLabel(),
		Proxy:            info.Egress,
		Egress:           info.Egress,
	}, h.Repo)
}

// extractRequestMessages extracts truncated messages from the request body for logging.
func extractRequestMessages(body []byte) []map[string]string {
	var req translator.OpenAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}
	return extractMessagesForLogging(req.Messages)
}

func extractMessagesForLogging(messages []translator.OpenAIMessage) []map[string]string {
	if len(messages) == 0 {
		return nil
	}
	msgs := make([]map[string]string, 0, len(messages))
	for _, m := range messages {
		content := extractContent(m.Content)
		content = truncateDetailText(content, constants.MaxMessageContentLen, "...")
		msgs = append(msgs, map[string]string{"role": m.Role, "content": content})
	}
	if len(msgs) > constants.MaxLoggedMessages {
		msgs = msgs[len(msgs)-constants.MaxLoggedMessages:]
	}
	return msgs
}

// extractContent handles content that is either a string or array of content blocks.
func extractContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, block := range v {
			if m, ok := block.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, " ")
	default:
		return fmt.Sprintf("%v", content)
	}
}

// mergeDailyUsage folds one successful request's contribution into the
// parsed day blob. It is the fold the batched accumulator in
// usage_daily.go applies per delta. (The one-shot ChatHandler wrappers
// that also called it were deleted in council round 2, R2-C-N1: no
// production caller remained — all traffic folds through the
// accumulator.)
func mergeDailyUsage(data map[string]any, provider, model, endpoint, connectionID, apiKey string, promptTokens, completionTokens, cachedTokens int, cost float64) {
	data["requests"] = getJSONInt(data, "requests") + 1
	data["promptTokens"] = getJSONInt(data, "promptTokens") + promptTokens
	data["completionTokens"] = getJSONInt(data, "completionTokens") + completionTokens
	data["cachedTokens"] = getJSONInt(data, "cachedTokens") + cachedTokens
	data["cost"] = getJSONFloat(data, "cost") + cost

	data["totalPromptTokens"] = getJSONInt(data, "totalPromptTokens") + promptTokens
	data["totalCompletionTokens"] = getJSONInt(data, "totalCompletionTokens") + completionTokens
	data["totalRequests"] = getJSONInt(data, "totalRequests") + 1

	// byProvider
	byProvider := getJSONMap(data, "byProvider")
	pp := getJSONMap(byProvider, provider)
	pp["requests"] = getJSONInt(pp, "requests") + 1
	pp["promptTokens"] = getJSONInt(pp, "promptTokens") + promptTokens
	pp["completionTokens"] = getJSONInt(pp, "completionTokens") + completionTokens
	pp["cachedTokens"] = getJSONInt(pp, "cachedTokens") + cachedTokens
	pp["cost"] = getJSONFloat(pp, "cost") + cost
	byProvider[provider] = pp
	data["byProvider"] = byProvider

	// byModel
	modelKey := model + "|" + provider
	byModel := getJSONMap(data, "byModel")
	mm := getJSONMap(byModel, modelKey)
	mm["requests"] = getJSONInt(mm, "requests") + 1
	mm["promptTokens"] = getJSONInt(mm, "promptTokens") + promptTokens
	mm["completionTokens"] = getJSONInt(mm, "completionTokens") + completionTokens
	mm["cachedTokens"] = getJSONInt(mm, "cachedTokens") + cachedTokens
	mm["cost"] = getJSONFloat(mm, "cost") + cost
	if mm["rawModel"] == nil {
		mm["rawModel"] = model
	}
	if mm["provider"] == nil {
		mm["provider"] = provider
	}
	byModel[modelKey] = mm
	data["byModel"] = byModel

	// byApiKey
	apiKeyVal := maskAPIKey(apiKey)
	if apiKeyVal == "" {
		apiKeyVal = "local-no-key"
	}
	akKey := apiKeyVal + "|" + model + "|" + provider
	byApiKey := getJSONMap(data, "byApiKey")
	ak := getJSONMap(byApiKey, akKey)
	ak["requests"] = getJSONInt(ak, "requests") + 1
	ak["promptTokens"] = getJSONInt(ak, "promptTokens") + promptTokens
	ak["completionTokens"] = getJSONInt(ak, "completionTokens") + completionTokens
	ak["cachedTokens"] = getJSONInt(ak, "cachedTokens") + cachedTokens
	ak["cost"] = getJSONFloat(ak, "cost") + cost
	ak["rawModel"] = model
	ak["provider"] = provider
	ak["apiKey"] = apiKeyVal
	byApiKey[akKey] = ak
	data["byApiKey"] = byApiKey

	// byEndpoint
	epKey := endpoint + "|" + model + "|" + provider
	byEndpoint := getJSONMap(data, "byEndpoint")
	ep := getJSONMap(byEndpoint, epKey)
	ep["requests"] = getJSONInt(ep, "requests") + 1
	ep["promptTokens"] = getJSONInt(ep, "promptTokens") + promptTokens
	ep["completionTokens"] = getJSONInt(ep, "completionTokens") + completionTokens
	ep["cachedTokens"] = getJSONInt(ep, "cachedTokens") + cachedTokens
	ep["cost"] = getJSONFloat(ep, "cost") + cost
	if ep["endpoint"] == nil {
		ep["endpoint"] = endpoint
	}
	if ep["rawModel"] == nil {
		ep["rawModel"] = model
	}
	if ep["provider"] == nil {
		ep["provider"] = provider
	}
	byEndpoint[epKey] = ep
	data["byEndpoint"] = byEndpoint

	// byAccount
	if connectionID != "" {
		byAccount := getJSONMap(data, "byAccount")
		acc := getJSONMap(byAccount, connectionID)
		acc["requests"] = getJSONInt(acc, "requests") + 1
		acc["promptTokens"] = getJSONInt(acc, "promptTokens") + promptTokens
		acc["completionTokens"] = getJSONInt(acc, "completionTokens") + completionTokens
		acc["cachedTokens"] = getJSONInt(acc, "cachedTokens") + cachedTokens
		acc["cost"] = getJSONFloat(acc, "cost") + cost
		if acc["rawModel"] == nil {
			acc["rawModel"] = model
		}
		if acc["provider"] == nil {
			acc["provider"] = provider
		}
		byAccount[connectionID] = acc
		data["byAccount"] = byAccount
	}

	// providers (legacy nested format)
	providers := getJSONMap(data, "providers")
	pd := getJSONMap(providers, provider)
	pd["promptTokens"] = getJSONInt(pd, "promptTokens") + promptTokens
	pd["completionTokens"] = getJSONInt(pd, "completionTokens") + completionTokens
	pd["requests"] = getJSONInt(pd, "requests") + 1

	models := getJSONMap(pd, "models")
	md := getJSONMap(models, model)
	md["promptTokens"] = getJSONInt(md, "promptTokens") + promptTokens
	md["completionTokens"] = getJSONInt(md, "completionTokens") + completionTokens
	md["requests"] = getJSONInt(md, "requests") + 1
	models[model] = md
	pd["models"] = models
	providers[provider] = pd
	data["providers"] = providers
}

// parseDailyData parses an existing daily JSON string into a mutable map.
func parseDailyData(raw string) map[string]any {
	if raw == "" {
		return make(map[string]any)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil || m == nil {
		return make(map[string]any)
	}
	return m
}

// jsonNumberAsFloat reads a numeric value out of a daily-aggregate map in
// whichever representation it currently holds. A map that came straight from
// parseDailyData carries float64 (JSON unmarshal), but the merge functions
// store Go int / float64 results back into the same map — so a second delta
// folded in the same batch (or any in-memory re-read) sees int, not float64.
// Accepting only float64 silently read those stored counts back as 0, which
// reset every integer counter to the latest delta on multi-delta folds while
// float fields (stored as float64) kept accumulating (found by the council
// round-1 C-1 probe: three chats folded to requests=1 with the last chat's
// token counts but the sum of all three costs).
func jsonNumberAsFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	}
	return 0, false
}

// getJSONInt extracts an int from a JSON map, whatever numeric form the
// value is stored in (see jsonNumberAsFloat).
func getJSONInt(m map[string]any, key string) int {
	if v, ok := m[key]; ok {
		if n, ok := jsonNumberAsFloat(v); ok {
			return int(n)
		}
	}
	return 0
}

func getJSONFloat(m map[string]any, key string) float64 {
	if v, ok := m[key]; ok {
		if n, ok := jsonNumberAsFloat(v); ok {
			return n
		}
	}
	return 0
}

// getJSONMap extracts or creates a nested map from a JSON map.
func getJSONMap(m map[string]any, key string) map[string]any {
	if v, ok := m[key]; ok {
		if sub, ok := v.(map[string]any); ok {
			return sub
		}
	}
	return make(map[string]any)
}

// maskAPIKey returns a masked version of an API key for storage. The canonical
// implementation lives in handlerutil so the usage-stats reader can derive the
// same mask from a stored row to recover the key's name.
func maskAPIKey(key string) string {
	return handlerutil.MaskAPIKey(key)
}

func resolveCompressionMode(ts *shared.TokenSaverConfig, savedTokens int) string {
	hasRTK := savedTokens > 0
	hasCaveman := false
	hasPonytail := false
	hasADHD := false

	if ts != nil {
		if ts.RTKEnabled() {
			hasRTK = true
		}
		hasCaveman = ts.CavemanEnabled()
		hasPonytail = ts.PonytailEnabled()
		hasADHD = ts.ADHDEnabled()
	}

	personaCount := 0
	if hasCaveman {
		personaCount++
	}
	if hasPonytail {
		personaCount++
	}
	if hasADHD {
		personaCount++
	}

	if (hasRTK && personaCount > 0) || personaCount > 1 {
		return "stacked"
	}
	if hasRTK {
		return "rtk"
	}
	if hasCaveman {
		return "caveman"
	}
	if hasADHD {
		return "adhd"
	}
	if hasPonytail {
		return "ponytail"
	}
	return "rtk"
}

// mergeDailyUsageError folds one failed request's contribution into the
// parsed day blob: the request/error counters and the provider/model
// breakdowns, with no token movement. It is the error fold the batched
// accumulator in usage_daily.go applies per delta.
func mergeDailyUsageError(data map[string]any, provider, model string) {
	data["requests"] = getJSONInt(data, "requests") + 1
	data["totalRequests"] = getJSONInt(data, "totalRequests") + 1
	data["errors"] = getJSONInt(data, "errors") + 1

	byProvider := getJSONMap(data, "byProvider")
	pp := getJSONMap(byProvider, provider)
	pp["requests"] = getJSONInt(pp, "requests") + 1
	pp["errors"] = getJSONInt(pp, "errors") + 1
	byProvider[provider] = pp
	data["byProvider"] = byProvider

	modelKey := model + "|" + provider
	byModel := getJSONMap(data, "byModel")
	mm := getJSONMap(byModel, modelKey)
	mm["requests"] = getJSONInt(mm, "requests") + 1
	mm["errors"] = getJSONInt(mm, "errors") + 1
	if mm["rawModel"] == nil {
		mm["rawModel"] = model
	}
	if mm["provider"] == nil {
		mm["provider"] = provider
	}
	byModel[modelKey] = mm
	data["byModel"] = byModel
}

func decDailyInt(data map[string]any, key string) {
	if v := getJSONInt(data, key); v > 0 {
		data[key] = v - 1
	}
}

// mergeDailyUsageRetract undoes one mergeDailyUsageError fold: the
// request it belonged to went on to succeed, so its error fold must not
// stand next to the success fold. Counters floor at zero — a retract
// whose original fold landed in an earlier flush generation whose blob
// was rebuilt must not drive a counter negative.
func mergeDailyUsageRetract(data map[string]any, provider, model string) {
	decDailyInt(data, "requests")
	decDailyInt(data, "totalRequests")
	decDailyInt(data, "errors")

	byProvider := getJSONMap(data, "byProvider")
	pp := getJSONMap(byProvider, provider)
	decDailyInt(pp, "requests")
	decDailyInt(pp, "errors")
	byProvider[provider] = pp
	data["byProvider"] = byProvider

	modelKey := model + "|" + provider
	byModel := getJSONMap(data, "byModel")
	mm := getJSONMap(byModel, modelKey)
	decDailyInt(mm, "requests")
	decDailyInt(mm, "errors")
	byModel[modelKey] = mm
	data["byModel"] = byModel
}
