package chat

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/models"
	"axonrouter/proxy/internal/providers"
	internalproxy "axonrouter/proxy/internal/proxy"
	"axonrouter/proxy/internal/proxy/poolstate"
	"axonrouter/proxy/internal/proxy/smart"
	"axonrouter/proxy/internal/translator"
)

func isRetryableComboError(ue *upstreamError) bool {
	if ue == nil {
		return false
	}
	if ue.StatusCode >= 400 && ue.StatusCode < 500 && ue.StatusCode != http.StatusTooManyRequests {
		return false
	}
	return true
}

func (h *ChatHandler) comboFallbackCombos(comboName string) []string {
	if h.Repo == nil || comboName == "" {
		return nil
	}
	settings, err := h.Repo.GetSettings()
	if err != nil || settings == nil {
		log.Warn("combo", "comboFallbackCombos GetSettings failed", "error", err)
		return nil
	}
	cfg, ok := settings.ComboStrategies[comboName]
	log.Info("combo", "comboFallbackCombos check", "comboName", comboName, "ok", ok, "fallbackCombos", cfg.FallbackCombos)
	if !ok {
		return nil
	}
	return cfg.FallbackCombos
}

// detectNewTurn reports whether the request body starts a new conversation
// turn. A turn boundary is the most recent plain-text user message; a request
// whose last user-type message is a tool result continues the current turn,
// and the combo must not switch providers mid-turn (Gemini thinking models
// require a thought_signature on every current-turn functionCall, which only
// the model that made the call can provide).
func detectNewTurn(body []byte) bool {
	var req struct {
		Messages []struct {
			Role    string         `json:"role"`
			Content jsontext.Value `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return true
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		msg := req.Messages[i]
		if msg.Role == "tool" {
			return false
		}
		if msg.Role != "user" {
			continue
		}
		return contentHasText(msg.Content)
	}
	return true
}

// contentHasText reports whether OpenAI message content contains plain text.
func contentHasText(content jsontext.Value) bool {
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s != ""
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err == nil {
		for _, p := range parts {
			if p.Text != "" {
				return true
			}
		}
	}
	return false
}

// visionProviders lists providers known to support vision/image input.
var visionProviders = map[string]bool{
	"openai":      true,
	"anthropic":   true,
	"claude":      true,
	"gemini":      true,
	"antigravity": true,
	"xai":         true,
	"mistral":     true,
	"groq":        true,
	"openrouter":  true,
}

// pdfProviders lists providers known to support PDF/document input.
var pdfProviders = map[string]bool{
	"openai":      true,
	"anthropic":   true,
	"claude":      true,
	"gemini":      true,
	"antigravity": true,
}

// modelHasCapability checks if a model (from a "provider/model" entry) supports
// the given capability. Returns true when uncertain (optimistic default).
func modelHasCapability(modelEntry string, cap string) bool {
	provider := modelEntry
	if idx := strings.Index(modelEntry, "/"); idx >= 0 {
		provider = modelEntry[:idx]
	}

	return capabilityFlag(providers.GetCapabilitiesForModel(provider, modelEntry), cap)
}

// DetectRequiredCapabilities extracts the capabilities a request needs.
//
// Modalities (vision/pdf/audioInput/videoInput) are scanned only on the current
// user turn — the trailing run after the last assistant/model message, which
// may span several messages. Media in older turns must not pin the combo to a
// vision model; that history is stripped or placeholdered downstream.
// "tools" is request-wide because it lives in the top-level tools array.
//
// Port of upstream detectRequiredCapabilities (open-sse/services/combo.js).
func DetectRequiredCapabilities(body []byte) map[string]bool {
	required := make(map[string]bool)

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return required
	}

	// OpenAI / Claude / Hermes / Ollama.
	for _, msg := range trailingUserItems(m["messages"]) {
		scanMessage(msg, required)
	}
	// Responses API.
	for _, item := range trailingUserItems(m["input"]) {
		scanContentBlocks(itemContent(item), required)
	}
	// Gemini / Antigravity.
	contents, _ := m["contents"].([]any)
	if len(contents) == 0 {
		if req, ok := m["request"].(map[string]any); ok {
			contents, _ = req["contents"].([]any)
		}
	}
	for _, c := range trailingUserItems(contents) {
		scanContentBlocks(itemParts(c), required)
	}

	if tools, ok := m["tools"].([]any); ok {
		for _, t := range tools {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			if tm["type"] == "function" || tm["function"] != nil || tm["functionDeclarations"] != nil {
				required["tools"] = true
				break
			}
		}
	}

	return required
}

// isAssistantRole reports whether a message is a model turn, which ends the
// current user run.
func isAssistantRole(role string) bool {
	return role == "assistant" || role == "model"
}

// trailingUserItems returns the trailing run of items after the last
// assistant/model entry — the current user turn. It may span several messages
// (text + image split across blocks).
func trailingUserItems(arr any) []any {
	list, ok := arr.([]any)
	if !ok || len(list) == 0 {
		return nil
	}
	i := len(list) - 1
	for i >= 0 {
		role := ""
		if m, ok := list[i].(map[string]any); ok {
			role, _ = m["role"].(string)
		}
		if isAssistantRole(role) {
			break
		}
		i--
	}
	return list[i+1:]
}

// itemParts reads a Gemini content entry's parts field.
func itemParts(item any) any {
	if m, ok := item.(map[string]any); ok {
		return m["parts"]
	}
	return nil
}

// scanMessage checks one message for capability requirements, covering the
// shapes that carry media outside the standard content array.
func scanMessage(msg any, required map[string]bool) {
	m, ok := msg.(map[string]any)
	if !ok {
		return
	}

	// Ollama / Hermes images array.
	if images, ok := m["images"].([]any); ok && len(images) > 0 {
		required["vision"] = true
	}

	// Vercel AI SDK / Hermes attachments / experimental_attachments.
	attachments, _ := m["experimental_attachments"].([]any)
	if len(attachments) == 0 {
		attachments, _ = m["attachments"].([]any)
	}
	for _, raw := range attachments {
		att, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		mime := firstString(att, "contentType", "mediaType")
		if mime == "" {
			if url, ok := att["url"].(string); ok {
				mime = dataURIMime(url)
			}
		}
		if mime != "" {
			addByMime(required, mime)
		} else if att["url"] != nil || att["data"] != nil {
			required["vision"] = true
		}
	}

	// Direct message-level modality properties.
	if m["image_url"] != nil || m["image"] != nil {
		required["vision"] = true
	}
	if m["audio_url"] != nil || m["audio"] != nil {
		required["audioInput"] = true
	}

	scanContentBlocks(m["content"], required)

	// Data URIs embedded in plain string content.
	if s, ok := m["content"].(string); ok {
		switch {
		case strings.Contains(s, "data:image/"):
			required["vision"] = true
		case strings.Contains(s, "data:audio/"):
			required["audioInput"] = true
		case strings.Contains(s, "data:application/pdf"):
			required["pdf"] = true
		}
	}
}

// scanContentBlocks walks a content array.
func scanContentBlocks(content any, required map[string]bool) {
	blocks, ok := content.([]any)
	if !ok {
		return
	}
	for _, b := range blocks {
		scanContentBlock(b, required)
	}
}

// scanContentBlock checks a single content block for capability requirements.
func scanContentBlock(block any, required map[string]bool) {
	b, ok := block.(map[string]any)
	if !ok {
		return
	}
	typ, _ := b["type"].(string)
	switch typ {
	case "image_url", "image", "input_image":
		required["vision"] = true
	case "input_audio", "audio_url", "audio":
		required["audioInput"] = true
	case "input_video", "video_url", "video":
		required["videoInput"] = true
	case "file", "document", "input_file":
		// Infer the modality from an embedded mime when available; fall back
		// to pdf for a generic file.
		if mime := fileBlockMime(b); mime != "" {
			addByMime(required, mime)
		} else {
			required["pdf"] = true
		}
	}

	// Gemini parts carry a mime on inlineData / fileData.
	if inner, ok := b["inlineData"].(map[string]any); ok {
		addByMime(required, stringOf(inner, "mimeType"))
	}
	if inner, ok := b["fileData"].(map[string]any); ok {
		addByMime(required, stringOf(inner, "mimeType"))
	}
}

// fileBlockMime digs a mime type out of the several shapes a file block uses
// to carry its payload.
func fileBlockMime(b map[string]any) string {
	if ia, ok := b["input_audio"].(map[string]any); ok {
		if format := stringOf(ia, "format"); format != "" {
			return "audio/" + format
		}
	}
	if file, ok := b["file"].(map[string]any); ok {
		if data := stringOf(file, "file_data"); data != "" {
			return dataURIMime(data)
		}
	}
	if source, ok := b["source"].(map[string]any); ok {
		if mime := stringOf(source, "media_type"); mime != "" {
			return mime
		}
		if data := stringOf(source, "data"); data != "" {
			return dataURIMime(data)
		}
	}
	return ""
}

// addByMime maps a mime type to its capability. An empty or unknown mime is
// ignored so an unrelated content type does not pin the combo.
func addByMime(required map[string]bool, mime string) {
	switch {
	case mime == "":
		return
	case strings.HasPrefix(mime, "image/"):
		required["vision"] = true
	case mime == "application/pdf":
		required["pdf"] = true
	case strings.HasPrefix(mime, "audio/"):
		required["audioInput"] = true
	case strings.HasPrefix(mime, "video/"):
		required["videoInput"] = true
	}
}

// dataURIMime extracts the mime from a "data:<mime>;base64,..." URL.
func dataURIMime(url string) string {
	rest, ok := strings.CutPrefix(url, "data:")
	if !ok {
		return ""
	}
	mime, _, ok := strings.Cut(rest, ",")
	if !ok {
		return ""
	}
	mime, _, _ = strings.Cut(mime, ";")
	return mime
}

// firstString returns the first key that holds a string value.
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v := stringOf(m, k); v != "" {
			return v
		}
	}
	return ""
}

// stringOf reads a string field, returning "" when absent or another type.
func stringOf(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// OrderByCredit stably sorts combo entries cheapest-credit-first: free
// (credit 0) first, then ascending credit, and unpriced entries (no credit
// in the provider's table — including every non-CodeBuddy-family provider)
// last, keeping their stored relative order. Port of upstream orderByCredit
// (open-sse/services/combo.js), reading the one credit table through
// providers.GetModelCredit.
func OrderByCredit(comboModels []string) []string {
	if len(comboModels) <= 1 {
		return comboModels
	}
	type ranked struct {
		entry  string
		idx    int
		credit float64
		priced bool
	}
	ranks := make([]ranked, len(comboModels))
	for i, entry := range comboModels {
		r := ranked{entry: entry, idx: i}
		if provider, model, found := strings.Cut(entry, "/"); found && provider != "" {
			r.credit, r.priced = providers.GetModelCredit(provider, model)
		}
		ranks[i] = r
	}
	sort.SliceStable(ranks, func(i, j int) bool {
		a, b := ranks[i], ranks[j]
		if a.priced != b.priced {
			return a.priced
		}
		if a.priced && b.priced && a.credit != b.credit {
			return a.credit < b.credit
		}
		return a.idx < b.idx
	})
	out := make([]string, len(ranks))
	for i, r := range ranks {
		out[i] = r.entry
	}
	return out
}

// comboCostAwareEnabled resolves the credit-ordering opt-out the way
// upstream chat.js does: the combo's own costAware flag, then the global
// settings.comboCostAware, then on.
func comboCostAwareEnabled(settings *db.SettingsData, comboName string) bool {
	if settings == nil {
		return true
	}
	if cfg, ok := settings.ComboStrategies[comboName]; ok && cfg.CostAware != nil {
		return *cfg.CostAware
	}
	if settings.ComboCostAware != nil {
		return *settings.ComboCostAware
	}
	return true
}

// ReorderByCapabilities stably floats models satisfying constraints to the
// front. Never drops a model, so the fallback chain stays intact.
//
// Tier 0: satisfies all hard AND all soft capabilities.
// Tier 1: satisfies every hard capability.
// Tier 2: the rest.
//
// Port of upstream reorderByCapabilities (open-sse/services/combo.js).
func ReorderByCapabilities(comboModels []string, required map[string]bool) []string {
	if len(required) == 0 || len(comboModels) <= 1 {
		return comboModels
	}

	hard := requiredHardCaps(required)
	soft := make([]string, 0, len(required))
	for cap := range required {
		if !slices.Contains(adapterCapabilityKeys, cap) {
			soft = append(soft, cap)
		}
	}

	tier := make(map[string]int, len(comboModels))
	order := make([]string, len(comboModels))
	for i, m := range comboModels {
		tier[m] = capabilityTier(m, hard, soft)
		order[i] = m
	}

	sort.SliceStable(order, func(i, j int) bool { return tier[order[i]] < tier[order[j]] })
	return order
}

// capabilityTier scores one model entry: 0 covers hard+soft, 1 covers hard
// only, 2 covers neither.
func capabilityTier(modelEntry string, hard, soft []string) int {
	caps := modelEntryCaps(modelEntry)
	for _, c := range hard {
		if !capabilityFlag(caps, c) {
			return 2
		}
	}
	for _, c := range soft {
		if !capabilityFlag(caps, c) {
			return 1
		}
	}
	return 0
}

// modelEntryCaps resolves the capability block for a "provider/model" entry.
func modelEntryCaps(modelEntry string) providers.Capabilities {
	provider, model, _ := strings.Cut(modelEntry, "/")
	return providers.GetCapabilitiesForModel(provider, model)
}

// comboRespectsStoredOrder reports whether this combo asked to skip the
// rotation strategy and walk its stored model list in order. The flag lives in
// the combo's own strategy row (settings.comboStrategies[name]) so it can
// differ per combo, the same place smartPolicy and sessionSticky live.
//
// A settings read that fails is treated as "not set" rather than as an error:
// the caller is mid-request with a combo to route, and the rotation strategy
// is the behaviour every combo had before the flag existed.
func (h *ChatHandler) comboRespectsStoredOrder(comboName string) bool {
	if h.Repo == nil || comboName == "" {
		return false
	}
	settings, err := h.Repo.GetSettings()
	if err != nil || settings == nil {
		return false
	}
	cfg, ok := settings.ComboStrategies[comboName]
	if !ok {
		return false
	}
	return cfg.RespectStoredOrder
}

// ApplyComboStrategy rotates the array of models based on the selected strategy.
// It treats every call as a new turn (backward-compatible wrapper).
func (h *ChatHandler) ApplyComboStrategy(strategy string, models []string, comboName string, stickyLimit int) []string {
	return h.applyComboStrategy(strategy, models, comboName, stickyLimit, true)
}

// applyComboStrategy is ApplyComboStrategy with turn awareness: the rotation
// index advances only on a new turn (newTurn=true), so a mid-turn tool-use
// sequence stays on the same provider/model.
func (h *ChatHandler) applyComboStrategy(strategy string, models []string, comboName string, stickyLimit int, newTurn bool) []string {
	if len(models) <= 1 {
		return models
	}

	switch strategy {
	case "round-robin", "roundrobin":
		if stickyLimit <= 0 {
			stickyLimit = 1
		}
		fallthrough
	case "sticky":
		if stickyLimit <= 0 {
			stickyLimit = 1
		}
		h.stickyMu.Lock()
		defer h.stickyMu.Unlock()
		if h.stickyState == nil {
			h.stickyState = make(map[string]*comboStickyState)
		}

		key := comboName
		if key == "" {
			key = "__default__"
		}
		state, exists := h.stickyState[key]
		if !exists {
			state = &comboStickyState{Index: 0, ConsecutiveUseCount: 0}
			h.stickyState[key] = state
		}

		// The model owning this turn. A new turn starts from the rotation
		// pointer; a mid-turn request reuses the model serving the turn even
		// after the pointer has advanced for the next turn.
		servingIndex := state.Index % len(models)
		if newTurn {
			// Advance the rotation pointer at the turn boundary.
			state.ConsecutiveUseCount++
			if state.ConsecutiveUseCount >= stickyLimit {
				state.Index = (servingIndex + 1) % len(models)
				state.ConsecutiveUseCount = 0
			}
			state.ServingIndex = servingIndex
		} else {
			servingIndex = state.ServingIndex
		}

		rotated := make([]string, len(models))
		for i := 0; i < len(models); i++ {
			rotated[i] = models[(servingIndex+i)%len(models)]
		}

		return rotated
	case "capacity":
		fallthrough
	default:
		out := make([]string, len(models))
		copy(out, models)
		return out
	}
}

// keysString returns a comma-separated list of map keys.
func keysString(m map[string]bool) string {
	return strings.Join(slices.Sorted(maps.Keys(m)), ",")
}

// upstreamErrorBody renders the terminal error a combo answers with when no
// member could serve the turn.
//
// The message is encoded by json.Marshal, never interpolated into a template:
// an upstream failure routinely carries characters that are illegal raw inside
// a JSON string — a URL that failed to parse embeds itself in quotes, a Windows
// path carries backslashes — and a format string emits them verbatim. The
// result is a body no client can parse: the first quote in the error text
// closes the string early and the following backslash becomes a bare token,
// which is what a JavaScript client reports as
// `JSON Parse error: Unrecognized token '\'`.
func upstreamErrorBody(message string) []byte {
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "upstream_error",
			"code":    http.StatusBadGateway,
		},
	})
	if err != nil {
		return []byte(`{"error":{"message":"upstream error","type":"upstream_error","code":502}}`)
	}
	return body
}

// comboLastTried tracks the index of the last entry a pass over models can
// still send a request for. The loop skips entries that do not resolve, a
// provider whose model is locked, and a provider whose every connection is
// excluded, cooling down or parked — none of which is knowable from
// len(models)-1. Keying the per-candidate timeout and the zero-content probe to
// that index cut off the one remaining attempt whenever the stored tail was
// skipped (#776).
//
// The answer also changes while the pass runs: a retryable failure locks a
// connection and adds it to excludeIDs, so an entry that was sendable when the
// pass started may be unreachable by the time the loop reaches it. Erring late
// is the failure — it arms the timer on the entry that really is last — so the
// index is recomputed whenever the exclusion set grows rather than once before
// the loop.
type comboLastTried struct {
	h      *ChatHandler
	models []string
	seen   []string
	idx    int
	valid  bool
}

// newComboLastTried returns a tracker for one pass over models.
func newComboLastTried(h *ChatHandler, models []string) *comboLastTried {
	return &comboLastTried{h: h, models: models, idx: -1}
}

// index reports the last sendable entry for the pass's current exclusions,
// recomputing only when they have changed since the last call.
func (t *comboLastTried) index(excludeIDs []string) int {
	if t.valid && slices.Equal(t.seen, excludeIDs) {
		return t.idx
	}
	t.seen = slices.Clone(excludeIDs)
	t.idx = t.lastSendableComboIndex(t.seen)
	t.valid = true
	return t.idx
}

// lastSendableComboIndex returns the index of the last entry a request could
// still be sent for under excludeIDs, or -1 when none can be.
func (t *comboLastTried) lastSendableComboIndex(excludeIDs []string) int {
	cache := make(map[string][]*models.ProviderConnection, len(t.models))
	for i := len(t.models) - 1; i >= 0; i-- {
		modelInfo := t.h.resolveModelEntry(t.models[i])
		if modelInfo == nil {
			continue
		}
		if t.h.comboEntrySendable(modelInfo, excludeIDs, cache) {
			return i
		}
	}
	return -1
}

// comboEntrySendable reports whether a pass could still send a request for one
// combo entry under excludeIDs. It mirrors the three skips the loop performs
// before forwarding: a provider with no active connection, a provider whose
// model is locked, and a provider whose every connection is excluded or
// cooling down. A provider that needs no credentials is always sendable, the
// same way the loop synthesizes its connection instead of selecting one.
//
// The connection query is memoised per pass: the predicate runs for every entry
// on every exclusion change, and re-querying a provider's rows each time would
// put the database back in the hot loop this tracker exists to keep it out of.
func (h *ChatHandler) comboEntrySendable(modelInfo *ModelInfo, excludeIDs []string, cache map[string][]*models.ProviderConnection) bool {
	if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
		return true
	}
	if h.Repo == nil {
		return true
	}
	conns, ok := cache[modelInfo.Provider]
	if !ok {
		var err error
		conns, err = h.Repo.GetProviderConnections(modelInfo.Provider, true)
		if err != nil {
			// An unreadable table is not evidence that the entry cannot run:
			// let the loop find out, which keeps this predicate from turning a
			// transient database error into a shorter combo.
			conns = nil
		}
		if len(conns) == 0 {
			if fallbackProvider, has := CredentialFallbacks[modelInfo.Provider]; has {
				conns, _ = h.Repo.GetProviderConnections(fallbackProvider, true)
			}
		}
		cache[modelInfo.Provider] = conns
	}
	if len(conns) == 0 {
		// No connection at all: the loop's getBestConnectionWithContext fails
		// and it moves on, so this entry cannot be the last one tried.
		return false
	}
	excluded := make(map[string]bool, len(excludeIDs))
	for _, id := range excludeIDs {
		excluded[id] = true
	}
	now := time.Now()
	for _, c := range conns {
		if c == nil || excluded[c.ID] {
			continue
		}
		if h.connectionModelBlocked(c, modelInfo.Provider, modelInfo.Model) {
			continue
		}
		if until, ok := db.ConnectionBlockedUntil(c.Data); ok && until.After(now) {
			continue
		}
		return true
	}
	return false
}

// handleComboFallback iterates through combo model entries, trying each one.
// Auto-capability-switch: floats vision/pdf-capable models to the front.
// adapterModels holds the capacity-adapter entries injected for this request;
// their history is trimmed to their context window before forwarding.
func (h *ChatHandler) handleComboFallback(ctx context.Context, w http.ResponseWriter, body []byte, comboModels []string, strategy string, isStream bool, translateResponse bool, comboName string, stickyLimit int, adapterModels ...string) {
	if FallbackAttemptRecorderFrom(ctx) == nil {
		ctx = WithFallbackAttemptRecorder(ctx, NewFallbackAttemptRecorder())
	}
	cw := newCommittedResponseWriter(w)
	cw.stream = isStream
	cw.responsesClient = translator.IsResponsesClient(ctx)
	h.handleComboFallbackWithVisited(ctx, cw, body, comboModels, strategy, isStream, translateResponse, comboName, stickyLimit, nil, true, adapterModels...)
}

func (h *ChatHandler) handleComboFallbackWithVisited(ctx context.Context, cw *committedResponseWriter, body []byte, comboModels []string, strategy string, isStream bool, translateResponse bool, comboName string, stickyLimit int, visited map[string]bool, isRoot bool, adapterModels ...string) {
	var lastErr *upstreamError
	var retry passRetry
	responseTimeout, timeoutGrace := h.comboTimeoutPolicy(comboName)
	modelTimedOut := false
	// Connections that failed with a retryable status this request; remaining
	// combo models must not re-select them (same account = same 429 quota).
	var excludeIDs []string

	var settings, _ = h.Repo.GetSettings()

	// 1. Apply combo rotation strategy first. The smart strategy orders the
	// models itself (System One evaluation + session stickiness) and carries
	// its routing state on the context for the failure loop below.
	//
	// RespectStoredOrder skips the rotation and walks the stored list as-is,
	// so the order the operator arranged in the editor is the fallback chain.
	// The smart and tokenmaxxing strategies are exempt: they compute dynamic
	// ranking (by task suitability and quota allowance respectively), so honoring
	// stored order would defeat the evaluation they exist to perform.
	var models []string
	if strategy == "smart" {
		ctx, models = h.applySmartRouting(ctx, body, comboModels, comboName)
	} else if strategy == "tokenmaxxing" {
		models = h.applyTokenmaxxingRouting(ctx, comboModels)
	} else {
		// Cost-aware base ordering (upstream handleComboChat): free →
		// cheapest first, applied to the stored list BEFORE rotation so a
		// fallback combo tries the cheap members first; the capability
		// reorder below stays the final authority. smart / tokenmaxxing are
		// exempt — they compute their own dynamic ranking and have no
		// upstream counterpart in this position.
		baseModels := comboModels
		if comboCostAwareEnabled(settings, comboName) {
			baseModels = OrderByCredit(comboModels)
			if len(baseModels) > 0 && len(comboModels) > 0 && baseModels[0] != comboModels[0] {
				log.Info("combo", "cost-aware reorder", "model", baseModels[0])
			}
		}
		if h.comboRespectsStoredOrder(comboName) {
			models = slices.Clone(baseModels)
		} else {
			models = h.applyComboStrategy(strategy, baseModels, comboName, stickyLimit, detectNewTurn(body))
		}
	}

	// 2. Auto-capability-switch: float models that satisfy the request's required capabilities to the front.
	// This ensures that if the rotated model lacks required capabilities (e.g., vision), a capable model overrides it.
	if required := DetectRequiredCapabilities(body); len(required) > 0 {
		reordered := ReorderByCapabilities(models, required)
		if reordered[0] != models[0] {
			log.Info("combo", "auto-switch", "caps", keysString(required), "model", reordered[0])
		}
		models = reordered
	}
	// A model that recently timed out sits out its grace period.
	models = h.withoutBenchedModels(comboName, models, timeoutGrace)

	// If every model fails, retry the whole pass once after a bounded
	// Retry-After wait so a transient provider blip doesn't surface as a hard
	// 429 to the client.
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			wait := retry.wait()
			if wait == 0 {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			// Fresh pass: re-allow connections locked by the previous attempt
			// (their cooldown has elapsed) and clear the error state.
			lastErr = nil
			retry.reset()
			excludeIDs = nil
		}

		// The entry tried last is the last one this pass can actually send a
		// request for, not the last one stored: unresolved entries, a
		// model-locked provider and a provider with nothing left to dial are
		// all skipped before anything is forwarded. The tracker recomputes
		// when this pass excludes another connection, so a member that the
		// pass itself takes out of reach stops counting as the last one.
		lastTried := newComboLastTried(h, models)

		for i, entry := range models {
			modelInfo := h.resolveModelEntry(entry)
			if modelInfo == nil {
				continue
			}
			// A System One decision model (Liquid d1 family) can never
			// serve a chat member: skip it without an upstream attempt.
			if providers.GetProviderModelKind(modelInfo.Provider, modelInfo.Model) == "systemone" {
				log.Warn("combo", "skip systemone member", "provider", modelInfo.Provider, "model", modelInfo.Model)
				if lastErr == nil {
					lastErr = &upstreamError{StatusCode: http.StatusNotFound, Body: []byte(`{"error":{"message":"member is a System One decision model and cannot serve chat requests","type":"not_found_error","code":404}}`)}
				}
				continue
			}
			isLastModel := i == lastTried.index(excludeIDs)

			// Skip unavailable (model-locked) providers
			if !h.Repo.IsProviderAvailable(modelInfo.Provider, modelInfo.Model) {
				log.Warn("combo", "skip unhealthy", "provider", modelInfo.Provider, "model", modelInfo.Model)
				if lastErr == nil {
					lastErr = &upstreamError{StatusCode: http.StatusTooManyRequests, Body: []byte(`{"error":{"message":"all connections for this provider are rate-limited","type":"rate_limit_error","code":429}}`)}
				}
				continue
			}

			candidateCtx := ctx
			var seatBudgetMs int
			if settings != nil {
				seatBudgetMs = settings.SeatAttemptBudgetMs
				if settings.ProviderOverrides != nil {
					if ov, ok := settings.ProviderOverrides[modelInfo.Provider]; ok && ov.SeatBudgetMs > 0 {
						seatBudgetMs = ov.SeatBudgetMs
					}
				}
			}
			if seatBudgetMs > 0 {
				candidateCtx = internalproxy.WithSeatBudget(candidateCtx, seatBudgetMs)
			}
			candidateWriter := http.ResponseWriter(cw)
			// The candidate tried last keeps the global deadline: the response
			// timeout only advances a combo, it never makes one fail sooner
			// than it would without the setting (#744).
			var timeoutGate *comboResponseTimeout
			if responseTimeout > 0 && !isLastModel {
				timeoutGate = newComboResponseTimeout(ctx, cw, responseTimeout)
				defer timeoutGate.finish()
				candidateCtx, candidateWriter = timeoutGate.ctx, timeoutGate
			}
			var entrySuccess bool
			// Connections this entry already sent the request to. A failure
			// that does not lock the account (a 400, a 500, a busy endpoint)
			// left it selectable, so without this the next connection attempt
			// picked the same account and resent the same request.
			var tried []string
			// Account attempts actually sent upstream for this entry,
			// capped per provider (comboMemberAttemptCap).
			memberAttempts := 0
			memberDecoyFails := 0
			attemptCap := comboMemberAttemptCap(modelInfo.Provider)

			if shouldHedge, hedgeDelay, maxAttempts := h.shouldHedgeProvider(modelInfo.Provider); shouldHedge && modelInfo.ConnectionID == "" && h.Repo != nil {
				allConns, cErr := h.Repo.GetProviderConnections(modelInfo.Provider, true)
				if cErr == nil && len(allConns) > 1 {
					candidates := h.pickHedgeCandidates(allConns, modelInfo.Provider, modelInfo.Model, maxAttempts)
					if len(candidates) > 1 {
						var upstreamBody map[string]any
						upstreamBodyJSON := adapterTrimmedBody(body, entry, adapterModels)
						if err := json.Unmarshal(upstreamBodyJSON, &upstreamBody); err == nil {
							upstreamBody["model"] = h.wireModelForNode(modelInfo.Provider, modelInfo.Model)
							repairToolCallIDsInMap(upstreamBody)
							candidateCtx = h.applyJailbreak(candidateCtx, upstreamBody, comboName, jailbreakLegIDs(modelInfo.Provider, modelInfo.Model)...)
							if upstreamJSON, err := json.Marshal(upstreamBody, json.Deterministic(true)); err == nil {
								targetWriter := candidateWriter
								fwdErr := h.executeHedged(candidateCtx, targetWriter, modelInfo.Provider, modelInfo.Model, comboName, comboName, upstreamJSON, isStream, translateResponse, forwardEndpoint(ctx, "/v1/chat/completions"), candidates, hedgeDelay, maxAttempts, h.hedgeAdmissionTimeout(modelInfo.Provider))
								if fwdErr == nil {
									return
								}
								var ue *upstreamError
								if errors.As(fwdErr, &ue) {
									retry.note(ue)
									lastErr = ue
								}
								continue
							}
						}
					}
				}
			}

			// Try up to 10 connections for this model entry
			for connAttempt := 0; connAttempt < 10; connAttempt++ {
				var connID string
				var picked *ProviderConnection
				var connData *ConnectionData
				isKnownNoAuth := false
				if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
					isKnownNoAuth = true
					connData = &ConnectionData{
						APIKey:      cfg.DefaultAPIKey,
						ProxyPoolID: h.ResolveProviderProxyPoolID(modelInfo.Provider),
					}
				} else {
					var cData *ConnectionData
					var err error
					picked, cData, err = h.getBestConnectionWithContext(candidateCtx, modelInfo.Provider, modelInfo.ConnectionID, entryExcludes(excludeIDs, tried), modelInfo.Model)
					if err != nil {
						// A node the gateway refuses on purpose — expired contract,
						// spent lifetime quota — must reach the client as that
						// refusal rather than as an anonymous "all combo models
						// failed": an operator can act on the first and not on the
						// second.
						if refusal, blocked := providerLimitRefusal(err); blocked {
							log.Info("combo", "provider node unusable, next candidate",
								"provider", modelInfo.Provider, "model", modelInfo.Model, "axis", refusal.Axis)
							var refusedErr *upstreamError
							errors.As(err, &refusedErr)
							if refusedErr != nil {
								lastErr = refusedErr
							}
						}
						break
					}
					connID = picked.ID
					connData = cData
				}
				// Skip a connection that is already locked for this model
				if connID != "" {
					lockKey := canonicalLockModel(modelInfo.Provider, modelInfo.Model)
					locked, _ := h.Repo.IsConnectionModelLocked(connID, lockKey)
					if !locked && lockKey != modelInfo.Model {
						locked, _ = h.Repo.IsConnectionModelLocked(connID, modelInfo.Model)
					}
					if locked {
						log.Warn("combo", "skip locked connection", "provider", modelInfo.Provider, "model", modelInfo.Model, "lockKey", lockKey, "conn", connID)
						excludeIDs = append(excludeIDs, connID)
						continue
					}
				}

				// The member's account-attempt budget is spent before the
				// request is built: a family member that already burned
				// its one account hands over to the next member here.
				if memberAttempts >= attemptCap {
					break
				}
				memberAttempts++

				var upstreamBody map[string]any
				upstreamBodyJSON := adapterTrimmedBody(body, entry, adapterModels)

				if err := json.Unmarshal(upstreamBodyJSON, &upstreamBody); err != nil {
					handlerutil.WriteJSONError(cw, http.StatusBadRequest, "failed to parse request body")
					return
				}
				upstreamBody["model"] = h.wireModelForNode(modelInfo.Provider, modelInfo.Model)
				repairToolCallIDsInMap(upstreamBody)
				candidateCtx = h.applyJailbreak(candidateCtx, upstreamBody, comboName, jailbreakLegIDs(modelInfo.Provider, modelInfo.Model)...)
				upstreamJSON, err := json.Marshal(upstreamBody, json.Deterministic(true))
				if err != nil {
					break
				}

				var fwdErr error
				fwdCtx := candidateCtx
				targetWriter := candidateWriter
				var streamPeeker *internalproxy.StreamContentPeeker
				if !isStream {
					fwdCtx = internalproxy.WithZeroContentProbe(candidateCtx, !isLastModel)
				} else if !isLastModel {
					streamPeeker = internalproxy.NewStreamContentPeeker(candidateWriter)
					targetWriter = streamPeeker
				}
				attemptStart := time.Now()
				fwdErr = h.tryForwardWithConnection(forwardRequestParams{
					Ctx: fwdCtx, W: targetWriter, Provider: modelInfo.Provider, Model: modelInfo.Model,
					RequestedModel: comboName, ComboName: comboName, Attempts: attemptCount(ctx),
					ConnectionID: connID, ConnName: connObjName(picked), ConnEmail: connObjEmail(picked),
					ConnData: connData, Body: upstreamJSON,
					IsStream: isStream, TranslateResponse: translateResponse,
					Endpoint: forwardEndpoint(ctx, "/v1/chat/completions"),
				})
				if timeoutGate != nil && timeoutGate.timedOut() && ctx.Err() == nil {
					fwdErr = context.DeadlineExceeded
				}
				if streamPeeker != nil && fwdErr == nil {
					if finishErr := streamPeeker.Finish(); finishErr != nil {
						fwdErr = finishErr
					}
				}
				RecordAttemptFromContext(ctx, connID, connObjName(picked), modelInfo.Provider, modelInfo.Model, 0, fwdErr, time.Since(attemptStart).Milliseconds())
				if fwdErr != nil && connID != "" {
					tried = append(tried, connID)
				}

				if fwdErr != nil {
					if timeoutGate != nil && timeoutGate.timedOut() && ctx.Err() == nil {
						log.Warn("combo", "model response timeout, falling back to next model", "provider", modelInfo.Provider, "model", modelInfo.Model)
						lastErr = comboTimeoutError()
						break
					}
					if errors.Is(fwdErr, internalproxy.ErrStreamingZeroContent) {
						log.Warn("combo", "streaming zero content, falling back to next model", "provider", modelInfo.Provider, "model", modelInfo.Model)
						lastErr = &upstreamError{StatusCode: http.StatusOK, Body: []byte(`{"error":{"message":"streaming zero content"}}`)}
						break
					}
					var zcErr *internalproxy.ZeroContentError
					if errors.As(fwdErr, &zcErr) {
						log.Warn("combo", "zero usable content, falling back to next model", "provider", modelInfo.Provider, "model", modelInfo.Model)
						lastErr = &upstreamError{StatusCode: http.StatusOK, Body: zcErr.Body}
						break
					}
					if errors.Is(fwdErr, translator.ErrDecoyOnlyResponse) {
						// A decoy-only answer is a model-behaviour mismatch
						// for this request shape, not an account fault: the
						// model reaches for the same cloaked decoy name on
						// every account (17 of 17 draws measured live on
						// 2026-11-10). One redraw covers variance; the
						// second ends this member's account loop.
						memberDecoyFails++
						if memberDecoyFails >= 2 {
							log.Warn("combo", "decoy-only responses repeated, falling back to next model", "provider", modelInfo.Provider, "model", modelInfo.Model)
							lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(`{"error":{"message":"member answered only with decoy tool calls"}}`)}
							break
						}
					}
					if ctx.Err() != nil {
						lastErr = &upstreamError{StatusCode: StatusClientClosedRequest, Body: []byte(clientClosedBody)}
						break
					}
					if isGuardrailBlock(fwdErr) {
						// A policy refusal is final: every other connection and
						// model in the combo would receive the same prompt and
						// answer with the same refused content, so continuing
						// would spread it across every provider the combo names.
						log.Warn("combo", "guardrail block, stopping turn", "provider", modelInfo.Provider, "conn", connID)
						var ue *upstreamError
						errors.As(fwdErr, &ue)
						lastErr = ue
						break
					}
					if attempt, refused := providerLimitRefusal(fwdErr); refused {
						// A provider at its own capacity is neither an account
						// fault nor a bad model: cooling the connection down
						// would park a healthy account, and the next candidate
						// may be free. Keep the refusal as the terminal error for
						// when none is left.
						log.Info("combo", "provider limit, next candidate",
							"provider", modelInfo.Provider, "model", modelInfo.Model,
							"axis", attempt.Axis, "conn", connID)
						var refusedErr *upstreamError
						errors.As(fwdErr, &refusedErr)
						if refusedErr != nil {
							lastErr = refusedErr
						}
						if isKnownNoAuth {
							break
						}
						continue
					}
					var ue *upstreamError
					if errors.As(fwdErr, &ue) {
						isLocErr := isProxyLocationError(fwdErr, func() []byte {
							if ue != nil {
								return ue.Body
							}
							return nil
						}())
						if isLocErr {
							poolID := connData.ResolvedProxyPoolID
							if poolID == "" {
								poolID = connData.ProxyPoolID
							}
							if poolID != "" && poolID != "__none__" && poolID != "direct" {
								smart.DefaultTracker.RecordFailureWithCooldown(modelInfo.Provider, poolID, 24*time.Hour, "FAILED_PRECONDITION: user location not supported")
								poolstate.MarkUnfit(poolID, modelInfo.Provider+"::*", time.Now().Add(24*time.Hour).UnixMilli(), "FAILED_PRECONDITION: user location not supported")
								log.Warn("combo", "proxy pool rejected by location policy, locked for 24h via smartfit",
									"conn", connID, "provider", modelInfo.Provider, "model", modelInfo.Model, "pool", poolID)
								continue
							}
						}
						// A 410 naming a dead model is a routing fact, not an account
						// fault: every connection to this provider answers the same
						// way. Record it, then leave this entry for the next combo
						// model (#179).
						//
						// The connection is deliberately NOT added to excludeIDs:
						// that list spans the whole pass, so excluding it here would
						// starve every later entry on a single-account provider —
						// exactly the case the failover exists to rescue. The model
						// lock recordModelDeprecation writes is per provider/model,
						// which is the scope that actually matters.
						if providers.IsModelDeprecation(ue.StatusCode, ue.Body) {
							h.recordModelDeprecation(ctx, modelInfo.Provider, modelInfo.Model, connID, ue)
							retry.note(ue)
							lastErr = ue
							break
						}
						// A prompt that does not fit this model fails the same
						// way on every connection to it, so advancing to the
						// next combo member is the only move that can succeed
						// (#338). Breaking here also skips
						// comboLockRetryable: the account is fine, and cooling
						// it down would take a healthy connection out of the
						// rotation.
						if providers.IsModelFitError(ue.StatusCode, ue.Body) {
							log.Info("combo", "model too small for request, next member",
								"provider", modelInfo.Provider, "model", modelInfo.Model)
							retry.note(ue)
							lastErr = ue
							break
						}
						if providers.IsModelConcurrencyError(ue.StatusCode, ue.Body) {
							log.Warn("combo", "model cluster concurrency saturated, next member",
								"provider", modelInfo.Provider, "model", modelInfo.Model)
							if modelInfo.Provider == "dahl" {
								RecordDahlModelConcurrencyBlocked(modelInfo.Model, 10*time.Second)
							}
							h.comboLockRetryable(ctx, &excludeIDs, connID, modelInfo.Provider, modelInfo.Model, ue)
							retry.note(ue)
							lastErr = ue
							break
						}
						if providers.IsUnsupportedMediaError(ue.StatusCode, ue.Body) {
							log.Warn("combo", "model rejected media format, next member",
								"provider", modelInfo.Provider, "model", modelInfo.Model)
							retry.note(ue)
							lastErr = ue
							break
						}
						if providers.RetryableStatusCodes[ue.StatusCode] {
							h.comboLockRetryable(ctx, &excludeIDs, connID, modelInfo.Provider, modelInfo.Model, ue)
						}
						if ue.StatusCode == http.StatusServiceUnavailable || ue.StatusCode == http.StatusBadGateway || ue.StatusCode == http.StatusGatewayTimeout {
							// In combo loops, fail over immediately to the next connection/model without blocking the client turn
							log.Info("combo", "transient failover skip", "status", ue.StatusCode, "provider", modelInfo.Provider, "conn", connID)
						}
						if isProxyTransportError(fwdErr, ue.StatusCode) {
							poolID := connData.ResolvedProxyPoolID
							if poolID == "" {
								poolID = connData.ProxyPoolID
							}
							if poolID != "" && poolID != "__none__" && poolID != "direct" {
								smart.DefaultTracker.RecordFailure(modelInfo.Provider, poolID, fwdErr)
								log.Warn("combo", "proxy pool transport error, penalizing fitness",
									"conn", connID, "provider", modelInfo.Provider, "model", modelInfo.Model, "pool", poolID, "status", ue.StatusCode)
							}
						}
						retry.note(ue)
						lastErr = ue
						if isKnownNoAuth {
							break
						}
						continue
					}
					lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: upstreamErrorBody(fmt.Sprintf("upstream error: %v", fwdErr))}
					if isKnownNoAuth {
						break
					}
					continue
				}

				entrySuccess = true
				if connData != nil {
					poolID := connData.ResolvedProxyPoolID
					if poolID == "" {
						poolID = connData.ProxyPoolID
					}
					if poolID != "" && poolID != "__none__" && poolID != "direct" {
						smart.DefaultTracker.RecordSuccess(modelInfo.Provider, poolID, time.Since(attemptStart))
					}
				}
				break
			}
			if timeoutGate != nil && timeoutGate.finish() && ctx.Err() == nil {
				lastErr = comboTimeoutError()
				modelTimedOut = true
				entrySuccess = false
				h.benchTimedOutModel(comboName, entry, timeoutGrace)
			}

			// A failed smart-pinned model loses its session pin, so the next
			// turn re-evaluates instead of returning to a dead model.
			if !entrySuccess {
				h.clearFailedSmartSticky(ctx, entry)
			}
			if entrySuccess || ctx.Err() != nil {
				return
			}
			if isGuardrailBlock(lastErr) {
				// A policy refusal ends the whole turn, not just this entry: the
				// next model would be sent the same prompt and answer with the
				// same refused content.
				break
			}
		}
		if isGuardrailBlock(lastErr) {
			break
		}

		// All entries failed. Retry once only if a bounded wait is available;
		// otherwise fall through to the error response below.
		if lastErr == nil || ctx.Err() != nil || attempt == 1 || modelTimedOut {
			break
		}
	}

	if cw.IsCommitted() {
		return
	}

	if lastErr != nil && isRetryableComboError(lastErr) {
		if fallbackCombos := h.comboFallbackCombos(comboName); len(fallbackCombos) > 0 {
			if visited == nil {
				visited = make(map[string]bool)
			}
			visited[comboName] = true
			for _, fbComboName := range fallbackCombos {
				if visited[fbComboName] {
					continue
				}
				if h.Repo == nil {
					continue
				}
				combo, err := h.Repo.GetComboByName(fbComboName)
				log.Info("combo", "trying fallback combo repo result", "fbComboName", fbComboName, "err", err, "comboNil", combo == nil)
				if err != nil || combo == nil || combo.Models == "" {
					continue
				}
				var fbModels []string
				if err := json.Unmarshal([]byte(combo.Models), &fbModels); err != nil || len(fbModels) == 0 {
					continue
				}
				flattened, err := h.flattenComboModels(fbModels)
				if err != nil || len(flattened) == 0 {
					continue
				}
				strat, sticky, _ := h.resolveComboRouting(combo.Name, combo.Strategy)

				nextVisited := make(map[string]bool, len(visited)+1)
				for k, v := range visited {
					nextVisited[k] = v
				}
				nextVisited[fbComboName] = true

				if cw.IsCommitted() {
					return
				}
				clearRouteHeaders(cw)

				h.handleComboFallbackWithVisited(ctx, cw, body, flattened, strat, isStream, translateResponse, fbComboName, sticky, nextVisited, false, adapterModels...)
				if cw.IsCommitted() {
					return
				}
			}
		}
	}

	if !isRoot {
		// As a child fallback combo, do not commit terminal error to cw so the parent
		// combo can continue trying its remaining fallbackCombos.
		return
	}

	if cw.IsCommitted() {
		return
	}
	if lastErr != nil {
		if cw.IsCommitted() {
			log.Error("combo", "upstream error after headers committed", "error", lastErr)
			return
		}
		retry.writeError(cw, lastErr)
		return
	}
	if cw.IsCommitted() {
		return
	}
	handlerutil.WriteJSONError(cw, http.StatusBadGateway, "all combo models failed: no valid entries")
}

// handleMessagesComboFallback iterates through combo models for the Claude endpoint.
// Auto-capability-switch: floats vision/pdf-capable models to the front.
// adapterModels holds the capacity-adapter entries injected for this request;
// their history is trimmed to their context window before forwarding.
func (h *ChatHandler) handleMessagesComboFallback(ctx context.Context, w http.ResponseWriter, translatedReq map[string]any, comboModels []string, strategy string, isStream bool, comboName string, stickyLimit int, adapterModels ...string) {
	if FallbackAttemptRecorderFrom(ctx) == nil {
		ctx = WithFallbackAttemptRecorder(ctx, NewFallbackAttemptRecorder())
	}
	cw := newCommittedResponseWriter(w)
	cw.stream = isStream
	cw.responsesClient = translator.IsResponsesClient(ctx)
	h.handleMessagesComboFallbackWithVisited(ctx, cw, translatedReq, comboModels, strategy, isStream, comboName, stickyLimit, nil, true, adapterModels...)
}

func (h *ChatHandler) handleMessagesComboFallbackWithVisited(ctx context.Context, cw *committedResponseWriter, translatedReq map[string]any, comboModels []string, strategy string, isStream bool, comboName string, stickyLimit int, visited map[string]bool, isRoot bool, adapterModels ...string) {
	var lastErr *upstreamError
	var retry passRetry
	responseTimeout, timeoutGrace := h.comboTimeoutPolicy(comboName)
	modelTimedOut := false

	// Auto-capability-switch: convert body to JSON for detection
	bodyJSON, _ := json.Marshal(translatedReq)
	// Connections that failed with a retryable status this request; remaining
	// combo models must not re-select them (same account = same 429 quota).
	var excludeIDs []string
	var models []string
	// smart and tokenmaxxing compute dynamic rankings and override stored order.
	if strategy == "smart" {
		ctx, models = h.applySmartRouting(ctx, bodyJSON, comboModels, comboName)
	} else if strategy == "tokenmaxxing" {
		models = h.applyTokenmaxxingRouting(ctx, comboModels)
	} else {
		// Cost-aware base ordering, same contract as handleComboFallback:
		// credit-first base order before rotation / stored order.
		var msgSettings *db.SettingsData
		if h.Repo != nil {
			msgSettings, _ = h.Repo.GetSettings()
		}
		baseModels := comboModels
		if comboCostAwareEnabled(msgSettings, comboName) {
			baseModels = OrderByCredit(comboModels)
		}
		if h.comboRespectsStoredOrder(comboName) {
			models = slices.Clone(baseModels)
		} else {
			models = h.applyComboStrategy(strategy, baseModels, comboName, stickyLimit, detectNewTurn(bodyJSON))
		}
	}
	if required := DetectRequiredCapabilities(bodyJSON); len(required) > 0 {
		reordered := ReorderByCapabilities(models, required)
		models = reordered
	}
	// A model that recently timed out sits out its grace period.
	models = h.withoutBenchedModels(comboName, models, timeoutGrace)

	// If every model fails, retry the whole pass once after a bounded
	// Retry-After wait so a transient provider blip doesn't surface as a hard
	// 429 to the client.
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			wait := retry.wait()
			if wait == 0 {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			// Fresh pass: re-allow connections locked by the previous attempt
			// (their cooldown has elapsed) and clear the error state.
			lastErr = nil
			retry.reset()
			excludeIDs = nil
		}

		// See the chat loop: the entry tried last is the last one this pass
		// can actually send a request for, not the last one stored.
		lastTried := newComboLastTried(h, models)

		for i, entry := range models {
			modelInfo := h.resolveModelEntry(entry)
			if modelInfo == nil {
				continue
			}
			// A System One decision model (Liquid d1 family) can never
			// serve a chat member: skip it without an upstream attempt.
			if providers.GetProviderModelKind(modelInfo.Provider, modelInfo.Model) == "systemone" {
				log.Warn("combo", "skip systemone member", "provider", modelInfo.Provider, "model", modelInfo.Model)
				if lastErr == nil {
					lastErr = &upstreamError{StatusCode: http.StatusNotFound, Body: []byte(`{"error":{"message":"member is a System One decision model and cannot serve chat requests","type":"not_found_error","code":404}}`)}
				}
				continue
			}
			isLastModel := i == lastTried.index(excludeIDs)

			// Skip unavailable (model-locked) providers
			if !h.Repo.IsProviderAvailable(modelInfo.Provider, modelInfo.Model) {
				log.Warn("combo", "skip unhealthy", "provider", modelInfo.Provider, "model", modelInfo.Model)
				if lastErr == nil {
					lastErr = &upstreamError{StatusCode: http.StatusTooManyRequests, Body: []byte(`{"error":{"message":"all connections for this provider are rate-limited","type":"rate_limit_error","code":429}}`)}
				}
				continue
			}
			candidateCtx := ctx
			candidateWriter := http.ResponseWriter(cw)
			// See the chat loop: the candidate tried last keeps the global deadline.
			var timeoutGate *comboResponseTimeout
			if responseTimeout > 0 && !isLastModel {
				timeoutGate = newComboResponseTimeout(ctx, cw, responseTimeout)
				defer timeoutGate.finish()
				candidateCtx, candidateWriter = timeoutGate.ctx, timeoutGate
			}

			var entrySuccess bool
			// See the chat loop: never resend this entry to a connection it
			// already tried.
			var tried []string
			// Account attempts actually sent upstream for this entry,
			// capped per provider (comboMemberAttemptCap).
			memberAttempts := 0
			memberDecoyFails := 0
			attemptCap := comboMemberAttemptCap(modelInfo.Provider)

			if shouldHedge, hedgeDelay, maxAttempts := h.shouldHedgeProvider(modelInfo.Provider); shouldHedge && modelInfo.ConnectionID == "" && h.Repo != nil {
				allConns, cErr := h.Repo.GetProviderConnections(modelInfo.Provider, true)
				if cErr == nil && len(allConns) > 1 {
					candidates := h.pickHedgeCandidates(allConns, modelInfo.Provider, modelInfo.Model, maxAttempts)
					if len(candidates) > 1 {
						entryBody, err := adapterTrimmedMap(translatedReq, entry, adapterModels)
						if err == nil {
							entryBody["model"] = h.wireModelForNode(modelInfo.Provider, modelInfo.Model)
							candidateCtx = h.applyJailbreak(candidateCtx, entryBody, comboName, jailbreakLegIDs(modelInfo.Provider, modelInfo.Model)...)
							if upstreamJSON, err := json.Marshal(entryBody, json.Deterministic(true)); err == nil {
								targetWriter := candidateWriter
								fwdErr := h.executeHedged(candidateCtx, targetWriter, modelInfo.Provider, modelInfo.Model, comboName, comboName, upstreamJSON, isStream, !translator.IsResponsesClient(ctx), forwardEndpoint(ctx, "/v1/v1/messages"), candidates, hedgeDelay, maxAttempts, h.hedgeAdmissionTimeout(modelInfo.Provider))
								if fwdErr == nil {
									return
								}
								var ue *upstreamError
								if errors.As(fwdErr, &ue) {
									retry.note(ue)
									lastErr = ue
								}
								continue
							}
						}
					}
				}
			}

			// Try up to 10 connections for this model entry
			for connAttempt := 0; connAttempt < 10; connAttempt++ {
				var connID string
				var picked *ProviderConnection
				var connData *ConnectionData
				isKnownNoAuth := false
				if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
					isKnownNoAuth = true
					connData = &ConnectionData{
						APIKey:      cfg.DefaultAPIKey,
						ProxyPoolID: h.ResolveProviderProxyPoolID(modelInfo.Provider),
					}
				} else {
					var cData *ConnectionData
					var err error
					picked, cData, err = h.getBestConnectionWithContext(candidateCtx, modelInfo.Provider, modelInfo.ConnectionID, entryExcludes(excludeIDs, tried), modelInfo.Model)
					if err != nil {
						if refusal, blocked := providerLimitRefusal(err); blocked {
							log.Info("combo", "provider node unusable, next candidate",
								"provider", modelInfo.Provider, "model", modelInfo.Model, "axis", refusal.Axis)
							var refusedErr *upstreamError
							errors.As(err, &refusedErr)
							if refusedErr != nil {
								lastErr = refusedErr
							}
						}
						break
					}
					connID = picked.ID
					connData = cData
				}
				if connID != "" {
					lockKey := canonicalLockModel(modelInfo.Provider, modelInfo.Model)
					locked, _ := h.Repo.IsConnectionModelLocked(connID, lockKey)
					if !locked && lockKey != modelInfo.Model {
						locked, _ = h.Repo.IsConnectionModelLocked(connID, modelInfo.Model)
					}
					if locked {
						log.Warn("combo", "skip locked connection", "provider", modelInfo.Provider, "model", modelInfo.Model, "lockKey", lockKey, "conn", connID)
						excludeIDs = append(excludeIDs, connID)
						continue
					}
				}

				// See the chat loop: the member's account-attempt budget
				// is spent before the request is built.
				if memberAttempts >= attemptCap {
					break
				}
				memberAttempts++

				entryBody, err := adapterTrimmedMap(translatedReq, entry, adapterModels)
				if err != nil {
					handlerutil.WriteJSONError(cw, http.StatusBadRequest, "failed to parse request body")
					return
				}
				entryBody["model"] = h.wireModelForNode(modelInfo.Provider, modelInfo.Model)
				candidateCtx = h.applyJailbreak(candidateCtx, entryBody, comboName, jailbreakLegIDs(modelInfo.Provider, modelInfo.Model)...)

				upstreamJSON, err := json.Marshal(entryBody, json.Deterministic(true))
				if err != nil {
					break
				}

				fwdCtx := candidateCtx
				targetWriter := candidateWriter
				var streamPeeker *internalproxy.StreamContentPeeker
				if !isStream {
					fwdCtx = internalproxy.WithZeroContentProbe(candidateCtx, !isLastModel)
				} else if !isLastModel {
					streamPeeker = internalproxy.NewStreamContentPeeker(candidateWriter)
					targetWriter = streamPeeker
				}
				attemptStart := time.Now()
				fwdErr := h.tryForwardWithConnection(forwardRequestParams{
					Ctx: fwdCtx, W: targetWriter, Provider: modelInfo.Provider, Model: modelInfo.Model,
					RequestedModel: comboName, ComboName: comboName, Attempts: attemptCount(ctx),
					ConnectionID: connID, ConnName: connObjName(picked), ConnEmail: connObjEmail(picked),
					ConnData: connData, Body: upstreamJSON,
					IsStream: isStream, TranslateResponse: !translator.IsResponsesClient(ctx),
					Endpoint: forwardEndpoint(ctx, "/v1/messages"),
				})
				if timeoutGate != nil && timeoutGate.timedOut() && ctx.Err() == nil {
					fwdErr = context.DeadlineExceeded
				}
				if streamPeeker != nil && fwdErr == nil {
					if finishErr := streamPeeker.Finish(); finishErr != nil {
						fwdErr = finishErr
					}
				}
				RecordAttemptFromContext(ctx, connID, connObjName(picked), modelInfo.Provider, modelInfo.Model, 0, fwdErr, time.Since(attemptStart).Milliseconds())
				if fwdErr != nil && connID != "" {
					tried = append(tried, connID)
				}

				if fwdErr != nil {
					if timeoutGate != nil && timeoutGate.timedOut() && ctx.Err() == nil {
						log.Warn("combo", "model response timeout, falling back to next model", "provider", modelInfo.Provider, "model", modelInfo.Model)
						lastErr = comboTimeoutError()
						break
					}
					if errors.Is(fwdErr, internalproxy.ErrStreamingZeroContent) {
						log.Warn("combo", "streaming zero content, falling back to next model", "provider", modelInfo.Provider, "model", modelInfo.Model)
						lastErr = &upstreamError{StatusCode: http.StatusOK, Body: []byte(`{"error":{"message":"streaming zero content"}}`)}
						break
					}
					var zcErr *internalproxy.ZeroContentError
					if errors.As(fwdErr, &zcErr) {
						log.Warn("combo", "zero usable content, falling back to next model", "provider", modelInfo.Provider, "model", modelInfo.Model)
						lastErr = &upstreamError{StatusCode: http.StatusOK, Body: zcErr.Body}
						break
					}
					if errors.Is(fwdErr, translator.ErrDecoyOnlyResponse) {
						// See the chat branch above: a decoy-only answer is
						// a model-behaviour mismatch, not an account fault;
						// one redraw covers variance, the second ends this
						// member's account loop.
						memberDecoyFails++
						if memberDecoyFails >= 2 {
							log.Warn("combo", "decoy-only responses repeated, falling back to next model", "provider", modelInfo.Provider, "model", modelInfo.Model)
							lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(`{"error":{"message":"member answered only with decoy tool calls"}}`)}
							break
						}
					}
					if ctx.Err() != nil {
						lastErr = &upstreamError{StatusCode: StatusClientClosedRequest, Body: []byte(clientClosedBody)}
						break
					}
					if isGuardrailBlock(fwdErr) {
						// See the chat branch above: a policy refusal stops the
						// turn instead of being replayed against the next model.
						log.Warn("combo", "guardrail block, stopping turn", "provider", modelInfo.Provider, "conn", connID)
						var blocked *upstreamError
						errors.As(fwdErr, &blocked)
						lastErr = blocked
						break
					}
					if attempt, refused := providerLimitRefusal(fwdErr); refused {
						// Same contract as the OpenAI loop: the endpoint is busy,
						// so the next candidate is tried without a cooldown on an
						// account that did nothing wrong.
						log.Info("combo", "provider limit, next candidate",
							"provider", modelInfo.Provider, "model", modelInfo.Model,
							"axis", attempt.Axis, "conn", connID)
						var refusedErr *upstreamError
						errors.As(fwdErr, &refusedErr)
						if refusedErr != nil {
							lastErr = refusedErr
						}
						if isKnownNoAuth {
							break
						}
						continue
					}
					var ue *upstreamError
					if errors.As(fwdErr, &ue) {
						// Same contract as the OpenAI loop: a 410 naming a dead
						// model is a provider-wide fact, so it is recorded and
						// the next combo entry is tried (#179). The connection
						// stays out of excludeIDs — see the OpenAI loop: that
						// list spans the pass, and a single-account provider
						// would have nothing left to fail over to.
						if providers.IsModelDeprecation(ue.StatusCode, ue.Body) {
							h.recordModelDeprecation(ctx, modelInfo.Provider, modelInfo.Model, connID, ue)
							retry.note(ue)
							lastErr = ue
							break
						}
						// A prompt that does not fit this model fails the same
						// way on every connection to it, so advancing to the
						// next combo member is the only move that can succeed
						// (#338). Breaking here also skips
						// comboLockRetryable: the account is fine, and cooling
						// it down would take a healthy connection out of the
						// rotation.
						if providers.IsModelFitError(ue.StatusCode, ue.Body) {
							log.Info("combo", "model too small for request, next member",
								"provider", modelInfo.Provider, "model", modelInfo.Model)
							retry.note(ue)
							lastErr = ue
							break
						}
						if providers.IsModelConcurrencyError(ue.StatusCode, ue.Body) {
							log.Warn("combo", "model cluster concurrency saturated, next member",
								"provider", modelInfo.Provider, "model", modelInfo.Model)
							if modelInfo.Provider == "dahl" {
								RecordDahlModelConcurrencyBlocked(modelInfo.Model, 10*time.Second)
							}
							h.comboLockRetryable(ctx, &excludeIDs, connID, modelInfo.Provider, modelInfo.Model, ue)
							retry.note(ue)
							lastErr = ue
							break
						}
						if providers.IsUnsupportedMediaError(ue.StatusCode, ue.Body) {
							log.Warn("combo", "model rejected media format, next member",
								"provider", modelInfo.Provider, "model", modelInfo.Model)
							retry.note(ue)
							lastErr = ue
							break
						}
						if providers.RetryableStatusCodes[ue.StatusCode] {
							h.comboLockRetryable(ctx, &excludeIDs, connID, modelInfo.Provider, modelInfo.Model, ue)
						}
						if ue.StatusCode == http.StatusServiceUnavailable || ue.StatusCode == http.StatusBadGateway || ue.StatusCode == http.StatusGatewayTimeout {
							// In combo loops, fail over immediately to the next connection/model without blocking the client turn
							log.Info("combo", "transient failover skip", "status", ue.StatusCode, "provider", modelInfo.Provider, "conn", connID)
						}
						retry.note(ue)
						lastErr = ue
						if isKnownNoAuth {
							break
						}
						continue
					}
					lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: upstreamErrorBody(fmt.Sprintf("upstream error: %v", fwdErr))}
					if isKnownNoAuth {
						break
					}
					continue
				}

				entrySuccess = true
				break
			}
			if timeoutGate != nil && timeoutGate.finish() && ctx.Err() == nil {
				lastErr = comboTimeoutError()
				modelTimedOut = true
				entrySuccess = false
				h.benchTimedOutModel(comboName, entry, timeoutGrace)
			}

			// A failed smart-pinned model loses its session pin, so the next
			// turn re-evaluates instead of returning to a dead model.
			if !entrySuccess {
				h.clearFailedSmartSticky(ctx, entry)
			}
			if entrySuccess || ctx.Err() != nil {
				return
			}
			if isGuardrailBlock(lastErr) {
				// See handleComboFallback: a policy refusal ends the turn.
				break
			}
		}
		if isGuardrailBlock(lastErr) {
			break
		}

		// All entries failed. Retry once only if a bounded wait is available;
		// otherwise fall through to the error response below.
		if lastErr == nil || ctx.Err() != nil || attempt == 1 || modelTimedOut {
			break
		}
	}

	if cw.IsCommitted() {
		return
	}

	if lastErr != nil && isRetryableComboError(lastErr) {
		if fallbackCombos := h.comboFallbackCombos(comboName); len(fallbackCombos) > 0 {
			if visited == nil {
				visited = make(map[string]bool)
			}
			visited[comboName] = true
			for _, fbComboName := range fallbackCombos {
				if visited[fbComboName] {
					continue
				}
				if h.Repo == nil {
					continue
				}
				combo, err := h.Repo.GetComboByName(fbComboName)
				log.Info("combo", "trying fallback combo repo result", "fbComboName", fbComboName, "err", err, "comboNil", combo == nil)
				if err != nil || combo == nil || combo.Models == "" {
					continue
				}
				var fbModels []string
				if err := json.Unmarshal([]byte(combo.Models), &fbModels); err != nil || len(fbModels) == 0 {
					continue
				}
				flattened, err := h.flattenComboModels(fbModels)
				if err != nil || len(flattened) == 0 {
					continue
				}
				strat, sticky, _ := h.resolveComboRouting(combo.Name, combo.Strategy)

				nextVisited := make(map[string]bool, len(visited)+1)
				for k, v := range visited {
					nextVisited[k] = v
				}
				nextVisited[fbComboName] = true

				if cw.IsCommitted() {
					return
				}
				clearRouteHeaders(cw)

				h.handleMessagesComboFallbackWithVisited(ctx, cw, translatedReq, flattened, strat, isStream, fbComboName, sticky, nextVisited, false, adapterModels...)
				if cw.IsCommitted() {
					return
				}
			}
		}
	}

	if !isRoot {
		// As a child fallback combo, do not commit terminal error to cw so the parent
		// combo can continue trying its remaining fallbackCombos.
		return
	}

	if cw.IsCommitted() {
		return
	}
	if lastErr != nil {
		if cw.IsCommitted() {
			log.Error("combo", "upstream error after headers committed", "error", lastErr)
			return
		}
		retry.writeError(cw, lastErr)
		return
	}
	if cw.IsCommitted() {
		return
	}
	handlerutil.WriteJSONError(cw, http.StatusBadGateway, "all combo models failed: no valid entries")
}

// entryExcludes returns the connections one combo entry must not select: the
// pass-wide exclusions plus the connections this entry already tried. The
// tried set stays per entry on purpose — a connection that rejected this
// model may serve the next one, and on a single-account provider excluding it
// for the whole pass would starve every later entry (see the 410 note in the
// chat loop). Clip forces a copy so excludeIDs is never written through.
func entryExcludes(excludeIDs, tried []string) []string {
	if len(tried) == 0 {
		return excludeIDs
	}
	return append(slices.Clip(excludeIDs), tried...)
}

// comboLockRetryable classifies a retryable upstream error in a combo loop and
// locks the failed connection+model so the exponential backoff persists across
// requests. The combo path iterates tryForwardWithConnection directly and used
// to skip this entirely — so a 429 never set a lock, every request re-tried all
// combo models on the same account, and Google rate-limited it forever. The
// connection is also appended to excludeIDs so the remaining combo models in
// this request skip it instead of re-hitting the same quota bucket.
// comboMemberAttemptCap is the per-member account-attempt budget of a
// combo entry (upstream COMBO_MEMBER_ACCOUNT_ATTEMPTS_BY_PROVIDER,
// fragment 977). The CodeBuddy family's risk control reacts to request
// velocity: a wide combo burning several accounts per member is exactly
// the burst the vendor answers with a 11140 restriction, so a family
// member burns ONE account before handing over to the next member.
// Every other provider keeps the loops' standing bound of 10. Only
// attempts that actually reach the upstream count — a connection
// skipped because it is already locked was never dialled.
func comboMemberAttemptCap(provider string) int {
	resolved := providers.ResolveAlias(provider)
	if providers.IsCodebuddyFamilyProvider(resolved) {
		return 1
	}
	if resolved == "tokenharbor" {
		return 2
	}
	return 10
}

func (h *ChatHandler) comboLockRetryable(ctx context.Context, excludeIDs *[]string, connID, provider, model string, ue *upstreamError) {
	if handlerutil.IsProbeContext(ctx) {
		*excludeIDs = append(*excludeIDs, connID)
		return
	}
	if connID == "" {
		return
	}
	// Same contract as the single-provider failover loop: an OpenCode free-tier
	// refusal is an egress verdict, not an account or model fault. Cooling
	// either would park a healthy account (and, for the 429 usage-limit shape,
	// hide it behind a quota it does not own), so the connection is only
	// excluded for the rest of this pass and the next combo entry is tried.
	if isOpencodeFreeTierRefusal(provider, ue) {
		*excludeIDs = append(*excludeIDs, connID)
		log.Warn("combo", "opencode free-tier refusal, not locking the account",
			"provider", provider, "model", model, "conn", connID, "status", ue.StatusCode)
		return
	}
	currentLevel := h.Repo.GetConnectionBackoffLevel(connID)
	errText := extractErrorText(ue.Body)
	errLower := strings.ToLower(errText)
	if strings.Contains(errLower, "validation_required") || strings.Contains(errLower, "verify your account") {
		if h.Repo != nil {
			_ = h.Repo.SetConnectionVerificationRequired(connID, "Verify your account to continue. PERMISSION_DENIED VALIDATION_REQUIRED")
		}
		h.getSessionAffinity().EvictConnection(connID)
		*excludeIDs = append(*excludeIDs, connID)
		return
	}
	if strings.Contains(errLower, "no project id") {
		if h.Repo != nil {
			_ = h.Repo.DisableConnectionWithReason(connID, http.StatusBadRequest, "antigravity: no project ID — onboard the account in Antigravity (antigravity.google) then re-login")
		}
		h.getSessionAffinity().EvictConnection(connID)
		*excludeIDs = append(*excludeIDs, connID)
		return
	}
	if strings.Contains(errLower, "invalid_grant") || strings.Contains(errLower, "token revoked") || strings.Contains(errLower, "refresh_token_reused") {
		if h.Repo != nil {
			_ = h.Repo.DisableConnectionWithReason(connID, http.StatusUnauthorized, "OAuth token revoked or expired: "+errText)
		}
		h.getSessionAffinity().EvictConnection(connID)
		*excludeIDs = append(*excludeIDs, connID)
		return
	}
	if providers.LooksLikeToSDisabled(ue.StatusCode, string(ue.Body)) || strings.Contains(errLower, "terms of service") || strings.Contains(errLower, "tos_violation") || strings.Contains(errLower, "account has been deleted") || strings.Contains(errLower, "account has been banned") {
		if h.Repo != nil {
			_ = h.Repo.DisableConnectionWithReason(connID, http.StatusForbidden, "Account disabled: "+errText)
		}
		h.getSessionAffinity().EvictConnection(connID)
		*excludeIDs = append(*excludeIDs, connID)
		return
	}
	if provider == "dahl" && (ue.StatusCode == http.StatusPaymentRequired || strings.Contains(errLower, "available tokens exhausted") || strings.Contains(errLower, "insufficient_quota") || strings.Contains(errLower, "invalid_api_key") || ue.StatusCode == http.StatusUnauthorized) {
		if h.Repo != nil {
			reason := "Dahl quota exhausted: " + errText
			if ue.StatusCode == http.StatusUnauthorized || strings.Contains(errLower, "invalid_api_key") {
				reason = "Dahl account expired or invalid key: " + errText
			}
			_ = h.Repo.DisableConnectionWithReason(connID, ue.StatusCode, reason)
		}
		h.getSessionAffinity().EvictConnection(connID)
		*excludeIDs = append(*excludeIDs, connID)
		return
	}

	var nodePolicy *providers.ErrorPolicy
	if h.Repo != nil && provider != "" {
		if _, nodeData, err := h.Repo.GetProviderNodeByID(provider); err == nil && nodeData != nil {
			nodePolicy = nodeData.ErrorPolicy
		}
	}
	cls := providers.ClassifyErrorWithPolicy(ue.StatusCode, errText, currentLevel, provider, nodePolicy)
	if cls.DeactivateKey {
		if h.Repo != nil {
			_ = h.Repo.DisableConnectionWithReason(connID, ue.StatusCode, "Disabled by provider error policy: "+errText)
		}
		h.getSessionAffinity().EvictConnection(connID)
		*excludeIDs = append(*excludeIDs, connID)
		return
	}
	// Upstream parity (checkFallbackError): request-scoped 4xx (400/404/405/
	// 409/422/...) must not lock the account — the bug is in the request,
	// not the credential. Only lock when ShouldFallback is set.
	if !cls.ShouldFallback || cls.CooldownMs <= 0 {
		*excludeIDs = append(*excludeIDs, connID)
		return
	}
	cooldownSec := cooldownSecForClassification(ue.StatusCode, cls, ue, provider)
	lockKey := canonicalLockModel(provider, model)
	// Banned-class verdicts (404 NOT_FOUND and its siblings) take the
	// durable lock twin: the quota auto-release must not resurrect a
	// model the upstream says this account does not have.
	lockFn := h.Repo.LockConnectionModel
	if cls.BannedAccount {
		lockFn = h.Repo.LockConnectionModelGone
	}
	if err := lockFn(connID, lockKey, cooldownSec, cls.NewBackoffLevel); err != nil {
		log.Warn("combo", "lock failed", "conn", connID, "provider", provider, "model", lockKey, "error", err)
	}
	if lockKey != model {
		_ = lockFn(connID, model, cooldownSec, cls.NewBackoffLevel)
	}
	// Account-scoped cooldown alongside the per-model locks, so the selector
	// can skip this account before spending a request (upstream applyErrorState).
	// Only lock if error is account-scoped, not model-scoped (e.g. 401 auth issues),
	// so unrelated models stay available. The classifier's ModelScoped
	// verdict wins over the text guess (see fallback.go).
	if cls.ModelScoped || isModelScopedError(ue.StatusCode, extractErrorText(ue.Body), model, ue.Body) {
		if recErr := h.Repo.RecordConnectionScopedError(connID, model, "chat", ue.StatusCode, extractErrorText(ue.Body), cls.NewBackoffLevel); recErr != nil {
			log.Warn("combo", "record connection error failed", "conn", connID, "error", recErr)
		}
	} else {
		until := time.Now().UTC().Add(time.Duration(cooldownSec) * time.Second)
		if err := h.Repo.LockConnectionRateLimit(connID, until, cls.NewBackoffLevel, ue.StatusCode, extractErrorText(ue.Body)); err != nil {
			log.Warn("combo", "rate limit lock failed", "conn", connID, "error", err)
		}
	}
	*excludeIDs = append(*excludeIDs, connID)
	log.Warn("combo", "locked on retryable error", "provider", provider, "model", model, "lockKey", lockKey, "conn", connID, "status", ue.StatusCode, "cooldown_s", cooldownSec)
}

// ---- Fusion (parallel fan-out + judge synthesis) ----

// fusionResult holds a single panel model's response.
type fusionResult struct {
	model string
	body  []byte
	ok    bool
	err   error
}

// FusionTuning tunes parallel-collection behavior of combo fusion.
// Matches JS FUSION_DEFAULTS in open-sse/services/combo.js.
type FusionTuning struct {
	MinPanel           int `json:"minPanel"`
	StragglerGraceMs   int `json:"stragglerGraceMs"`
	PanelHardTimeoutMs int `json:"panelHardTimeoutMs"`
}

var fusionDefaults = FusionTuning{
	MinPanel:           2,
	StragglerGraceMs:   8000,
	PanelHardTimeoutMs: 90000,
}

// handleFusion implements combo fusion: parallel model fan-out + judge synthesis.
// Matches JS handleFusionChat in combo.js.
func (h *ChatHandler) handleFusion(ctx context.Context, w http.ResponseWriter, body []byte, comboModels []string, strategy string, isStream bool, translateResponse bool, comboName string, stickyLimit int, customJudgeModel string) {
	cw := newCommittedResponseWriter(w)
	panel := h.ApplyComboStrategy(strategy, comboModels, comboName, stickyLimit)
	if len(panel) == 0 {
		handlerutil.WriteJSONError(cw, http.StatusBadRequest, "fusion combo has no models")
		return
	}
	if len(panel) == 1 {
		h.handleComboFallback(ctx, cw, body, panel, "fallback", isStream, translateResponse, comboName, stickyLimit)
		return
	}

	// Build panel body: strip tools → prose, force non-streaming
	var panelBody map[string]any
	if err := json.Unmarshal(body, &panelBody); err != nil {
		handlerutil.WriteJSONError(cw, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if msgs, ok := panelBody["messages"].([]any); ok {
		panelBody["messages"] = flattenToolHistory(msgs)
	}
	panelBody["stream"] = false
	delete(panelBody, "tools")
	delete(panelBody, "tool_choice")
	panelJSON, err := json.Marshal(panelBody)
	if err != nil {
		handlerutil.WriteJSONError(cw, http.StatusInternalServerError, "failed to marshal panel body")
		return
	}

	// Fan-out panel calls (ctx aborts stragglers the panel moves on without).
	ft := fusionDefaults
	calls := make([]func(context.Context) *fusionResult, len(panel))
	for i, entry := range panel {
		calls[i] = h.makePanelCall(panelJSON, entry, comboName)
	}

	settled := collectPanel(ctx, calls, ft)

	// Extract successful answers
	var answers []fusionAnswer
	judgeModel := panel[0] // default: first panel model
	if customJudgeModel != "" {
		judgeModel = customJudgeModel
	}
	for i, res := range settled {
		if res == nil || !res.ok {
			continue
		}
		text := extractPanelText(res.body)
		if text == "" {
			continue
		}
		answers = append(answers, fusionAnswer{model: panel[i], text: text})
	}

	// Degradation
	if len(answers) == 0 {
		handlerutil.WriteJSONError(cw, http.StatusServiceUnavailable, "all fusion panel models failed")
		return
	}
	if len(answers) == 1 {
		h.handleComboFallback(ctx, cw, body, []string{answers[0].model}, "fallback", isStream, translateResponse, comboName, stickyLimit)
		return
	}

	// Judge synthesizes final answer
	judgeBody := appendUserTurn(body, buildJudgePrompt(answers))

	var ub map[string]any
	if err := json.Unmarshal(judgeBody, &ub); err != nil {
		log.Error("fusion", "unmarshal judge body failed", "error", err)
		handlerutil.WriteJSONError(cw, http.StatusInternalServerError, "failed to parse judge request")
		return
	}
	ub["model"] = judgeModel
	ctx = h.applyJailbreak(ctx, ub, comboName, judgeModel)
	if isStream {
		ub["stream"] = true
	} else {
		delete(ub, "stream")
	}
	judgeJSON, err := json.Marshal(ub)
	if err != nil {
		log.Error("combo", "marshal judge body failed", "error", err)
		handlerutil.WriteJSONError(cw, http.StatusInternalServerError, "failed to marshal judge request")
		return
	}

	modelInfo := h.resolveModelEntry(judgeModel)
	if modelInfo == nil {
		handlerutil.WriteJSONError(cw, http.StatusBadGateway, fmt.Sprintf("unresolved judge model: %s", judgeModel))
		return
	}
	h.handleSingleModel(ctx, cw, judgeJSON, modelInfo, isStream, translateResponse)
}

// ResetComboState clears the sticky state for combos
func (h *ChatHandler) ResetComboState(comboName string) {
	h.stickyMu.Lock()
	defer h.stickyMu.Unlock()
	if h.stickyState == nil {
		h.stickyState = make(map[string]*comboStickyState)
	}
	if comboName != "" {
		delete(h.stickyState, comboName)
	} else {
		h.stickyState = make(map[string]*comboStickyState)
	}
}
