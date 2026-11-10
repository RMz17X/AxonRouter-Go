package chat

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/providers"
)

// Smart Router (System One) combo strategy, ported from upstream
// open-sse/services/systemoneRouter.js + src/sse/services/systemoneRouter.js
// (f3f86fdd, #4975): a decision model evaluates the request before dispatch.
// Deliberate divergence from upstream (owner's call, 2026-11-11): upstream
// had the router pick one MODEL out of the combo; here the pools are the
// operator's own classification, so the router only judges which TIER the
// request belongs to — easy or hard — and the chosen tier's members lead
// in combo order. Per-session stickiness still pins a concrete model so
// later turns reuse the provider holding the warm prompt cache. Everything
// here fails open: any evaluation problem leaves the combo's own ordering
// in place, so a broken router model can never take the combo down.

const (
	// smartRouterDefaultModel is the fallback System One model when a combo
	// does not explicitly configure a routerModel.
	smartRouterDefaultModel = "liquid/d1"
	// smartRouterTimeout bounds one evaluation; past it the strategy falls
	// back to the combo's own ordering rather than stalling the turn.
	smartRouterTimeout = 8 * time.Second
	// smartMaxStateChars caps the prompt text sent to the classifier.
	smartMaxStateChars = 2000
	// smartStickyTTL is how long a session's pinned model survives idle.
	smartStickyTTL = 2 * time.Hour
	// smartMaxStickySessions bounds the sticky store; at the ceiling the
	// oldest quarter is dropped (upstream pruneStickyComboSessions).
	smartMaxStickySessions = 5000
	// smartDefaultThreshold is the Smart Switch confidence floor.
	smartDefaultThreshold = 0.8
)

// smartChoice is one parsed System One routing decision: the tier the
// router picked ("easy" or "hard") and its confidence in that call.
type smartChoice struct {
	Choice     string
	Confidence *float64
}

// smartRouteState rides the request context so the combo failure loop can
// clear a pinned model that just failed, without threading the smart fields
// through every signature between dispatch and the per-entry error path.
type smartRouteState struct {
	comboName   string
	sessionID   string
	activeModel string
}

type smartRouteStateKey struct{}

// smartDecision is the per-request record of what the smart router did,
// merged into the request detail (see smartDecisionDetailField) so the
// usage page can show whether the AI sent the request to the easy tier
// or the hard tier, or whether routing fell back to the combo's stored
// order. Verdict is the tier of the model the router led with; Source
// names the path that produced it.
type smartDecision struct {
	Verdict    string   `json:"verdict"` // "easy" | "hard" | "fallback"
	Source     string   `json:"source"`  // "selected" | "switch" | "retained" | "strict" | "below-threshold" | "no-decision" | "heavy-session"
	Model      string   `json:"model,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
	Threshold  float64  `json:"threshold,omitempty"`
}

type smartDecisionKey struct{}

func withSmartDecision(ctx context.Context, d *smartDecision) context.Context {
	return context.WithValue(ctx, smartDecisionKey{}, d)
}

func smartDecisionFrom(ctx context.Context) *smartDecision {
	d, _ := ctx.Value(smartDecisionKey{}).(*smartDecision)
	return d
}

// smartTierOf names the tier a model belongs to for the decision record:
// the operator's pool lists win; unlisted models fall to the routing
// heuristic inside isEasyTierModel.
func smartTierOf(model string, cfg db.ComboStrategy) string {
	if isEasyTierModel(model, cfg) {
		return "easy"
	}
	return "hard"
}

// smartHeavySessionChars is the conversation-text size past which a
// session counts as a heavy working session on its own, regardless of
// tool use: far beyond casual chat, far below a real coding session
// (the owner's contexts run past 100K tokens).
const smartHeavySessionChars = 48000

// smartSessionInfo is the local, deterministic read of a request body:
// what kind of session this turn belongs to. The router model only
// ever sees the latest user turn, so on its own it cannot tell a
// trivial standalone question from a two-word steering message ("go
// on", "fix that") in the middle of a long, heavy task — exactly the
// turns a coding session is made of (owner report, 2026-11-10). These
// signals are computed from the body the gateway already holds, for
// free, and feed both the router's state text and the heavy-session
// guard in applySmartRouting.
type smartSessionInfo struct {
	Messages       int
	TextChars      int
	ToolsInUse     bool
	LastTurnIsTool bool
	Heavy          bool
}

type smartBodyViewKey struct{}

// smartBodyView bundles the session analysis and the enriched router
// state computed from one parse of the body. applySmartRouting computes
// it once and rides it on the context so resolveSmartTier does not
// parse the (possibly very large) body again.
type smartBodyView struct {
	info  smartSessionInfo
	state string
}

// viewSmartBody parses the body once and derives the session info and
// the state text sent to the router model: a one-line session summary
// followed by the most recent user turns (up to three, oldest first,
// each truncated). The summary is what lets the router know a short
// last turn belongs to a long tool-driven session instead of reading
// it as a trivial standalone request.
func viewSmartBody(body []byte) smartBodyView {
	var raw map[string]any
	if json.Unmarshal(body, &raw) != nil || raw == nil {
		return smartBodyView{state: extractSmartPromptState(body)}
	}
	info := smartSessionInfo{}
	if tools, ok := raw["tools"].([]any); ok && len(tools) > 0 {
		info.ToolsInUse = true
	}
	msgs, _ := raw["messages"].([]any)
	isInput := false
	if len(msgs) == 0 {
		if input, ok := raw["input"].([]any); ok {
			msgs = input
			isInput = true
		}
	}
	info.Messages = len(msgs)
	var userTurns []string
	for i, m := range msgs {
		msg, _ := m.(map[string]any)
		if msg == nil {
			continue
		}
		info.TextChars += len(smartTextOf(msg["content"]))
		if smartMsgUsesTools(msg) {
			info.ToolsInUse = true
		}
		if i == len(msgs)-1 && smartMsgIsToolResult(msg) {
			info.LastTurnIsTool = true
		}
		isUser := false
		if isInput {
			t, _ := msg["type"].(string)
			role, _ := msg["role"].(string)
			isUser = t == "message" && (role == "" || role == "user")
		} else {
			role, _ := msg["role"].(string)
			isUser = role == "user"
		}
		if isUser {
			if text := strings.TrimSpace(smartTextOf(msg["content"])); text != "" {
				userTurns = append(userTurns, text)
			}
		}
	}
	info.Heavy = info.ToolsInUse || info.TextChars > smartHeavySessionChars

	header := fmt.Sprintf("[session: %d messages, ~%d chars of conversation", info.Messages, info.TextChars)
	if info.ToolsInUse {
		header += ", tools in use"
	}
	if info.LastTurnIsTool {
		header += ", last turn is a tool result"
	}
	header += "]"

	if len(userTurns) == 0 {
		return smartBodyView{info: info, state: extractSmartPromptState(body)}
	}
	if len(userTurns) > 3 {
		userTurns = userTurns[len(userTurns)-3:]
	}
	parts := make([]string, 0, len(userTurns)+1)
	parts = append(parts, header)
	for _, turn := range userTurns {
		if len(turn) > 700 {
			turn = turn[:700]
		}
		parts = append(parts, turn)
	}
	return smartBodyView{info: info, state: strings.Join(parts, "\n---\n")}
}

// smartMsgUsesTools reports whether one conversation item shows tool
// use: an assistant tool call, a tool/function result message, a
// Responses-API function call or its output, or a tool_result block.
func smartMsgUsesTools(msg map[string]any) bool {
	if _, ok := msg["tool_calls"]; ok {
		return true
	}
	if role, _ := msg["role"].(string); role == "tool" || role == "function" {
		return true
	}
	if t, _ := msg["type"].(string); t == "function_call" || t == "function_call_output" {
		return true
	}
	return smartMsgIsToolResult(msg)
}

// smartMsgIsToolResult reports whether one conversation item is a
// tool result: a tool/function role, a function_call_output item, or
// content carrying a tool_result block.
func smartMsgIsToolResult(msg map[string]any) bool {
	if role, _ := msg["role"].(string); role == "tool" || role == "function" {
		return true
	}
	if t, _ := msg["type"].(string); t == "function_call_output" {
		return true
	}
	if parts, ok := msg["content"].([]any); ok {
		for _, p := range parts {
			if pm, ok := p.(map[string]any); ok {
				if pt, _ := pm["type"].(string); pt == "tool_result" {
					return true
				}
			}
		}
	}
	return false
}

// extractSmartPromptState pulls the latest user turn out of a request body
// (messages[] or input[] or a string prompt), truncated to the classifier
// cap. Upstream extractPromptState.
func extractSmartPromptState(body []byte) string {
	var raw map[string]any
	if json.Unmarshal(body, &raw) != nil || raw == nil {
		return ""
	}
	if messages, ok := raw["messages"].([]any); ok && len(messages) > 0 {
		for i := len(messages) - 1; i >= 0; i-- {
			msg, _ := messages[i].(map[string]any)
			if msg == nil {
				continue
			}
			role, _ := msg["role"].(string)
			if role == "user" || !hasRoleAndNoContent(msg) {
				if text := smartTextOf(msg["content"]); strings.TrimSpace(text) != "" {
					return truncateSmartState(text)
				}
			}
		}
		return truncateSmartState(smartTextOf(lastSmartMessageContent(messages)))
	}
	if input, ok := raw["input"].([]any); ok && len(input) > 0 {
		for i := len(input) - 1; i >= 0; i-- {
			msg, _ := input[i].(map[string]any)
			if msg == nil {
				continue
			}
			if t, _ := msg["type"].(string); t == "message" {
				if text := smartTextOf(msg["content"]); strings.TrimSpace(text) != "" {
					return truncateSmartState(text)
				}
			}
		}
		return truncateSmartState(smartTextOf(lastSmartMessageContent(input)))
	}
	if s, ok := raw["prompt"].(string); ok && strings.TrimSpace(s) != "" {
		return truncateSmartState(s)
	}
	if s, ok := raw["input"].(string); ok && strings.TrimSpace(s) != "" {
		return truncateSmartState(s)
	}
	return ""
}

func hasRoleAndNoContent(msg map[string]any) bool {
	role, _ := msg["role"].(string)
	return role != "" && msg["content"] == nil
}

func lastSmartMessageContent(messages []any) any {
	if msg, _ := messages[len(messages)-1].(map[string]any); msg != nil {
		if c, ok := msg["content"]; ok {
			return c
		}
		return msg["text"]
	}
	return nil
}

func smartTextOf(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, part := range v {
			switch p := part.(type) {
			case string:
				parts = append(parts, p)
			case map[string]any:
				if t, _ := p["type"].(string); t == "text" || t == "input_text" || t == "output_text" {
					if text, ok := p["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func truncateSmartState(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > smartMaxStateChars {
		return text[:smartMaxStateChars]
	}
	return text
}

const (
	// smartHardCriteria describes the hard tier to the System One router:
	// models the operator listed in the combo's hardModels, plus any model
	// the capability heuristics identify as a reasoning model.
	smartHardCriteria = "Advanced reasoning model suited for complex algorithms, deep logic, multi-step problem solving, math, system architecture, and hard tasks."
	// smartEasyCriteria describes the easy tier: models the operator
	// listed in easyModels, plus name-heuristic lightweight models.
	smartEasyCriteria = "Fast, cost-efficient, and lightweight model best suited for straightforward requests, simple syntax, one-liners, basic scripts, and quick tasks."
)

// describeModelForRouting derives capability-aware description criteria for a model
// so that any configured System One router model (Liquid d1, d1.1, d1-ultra, or
// future decision providers) has discriminative context to select the best match.
func describeModelForRouting(modelStr string, isEasy, isHard bool) string {
	if isHard {
		return "Advanced reasoning model (Hard tier) designated for deep reasoning, complex logic, multi-step problem solving, math, system architecture, and hard tasks."
	}
	if isEasy {
		return "Fast, cost-efficient, and lightweight model (Easy tier) designated for straightforward requests, simple syntax, one-liners, basic scripts, and quick tasks."
	}

	parts := strings.SplitN(modelStr, "/", 2)
	provider := ""
	model := modelStr
	if len(parts) == 2 {
		provider = parts[0]
		model = parts[1]
	}

	caps := providers.GetCapabilitiesForModel(provider, model)
	mLower := strings.ToLower(model)

	if caps.Reasoning || strings.Contains(mLower, "r1") || strings.Contains(mLower, "o1") || strings.Contains(mLower, "o3") || strings.Contains(mLower, "reasoning") || strings.Contains(mLower, "24b") || strings.Contains(mLower, "opus") || strings.Contains(mLower, "sonnet") {
		return smartHardCriteria
	}
	if caps.Vision {
		return "Multimodal vision model capable of processing images, diagrams, charts, and visual content."
	}
	if strings.Contains(mLower, "flash") || strings.Contains(mLower, "mini") || strings.Contains(mLower, "1.7b") || strings.Contains(mLower, "8b") || strings.Contains(mLower, "haiku") || strings.Contains(mLower, "nano") || strings.Contains(mLower, "small") || strings.Contains(mLower, "lite") || strings.Contains(mLower, "fast") {
		return smartEasyCriteria
	}
	return "Standard language model suited for general text generation, basic coding, conversation, and general knowledge."
}

// partitionByTier splits combo members into the easy and hard pools:
// the operator's pool lists win; unlisted members fall to the routing
// heuristic inside isEasyTierModel.
func partitionByTier(models []string, cfg db.ComboStrategy) (easy, hard []string) {
	for _, m := range models {
		if isEasyTierModel(m, cfg) {
			easy = append(easy, m)
		} else {
			hard = append(hard, m)
		}
	}
	return easy, hard
}

// buildSmartTierCriteria derives the choices and criteria for a tier
// evaluation: the router judges the REQUEST, not the members — the
// pools are already the operator's classification, so the only open
// question is which tier this turn belongs to. Each tier's criteria
// names its members so the router knows what it is choosing between.
func buildSmartTierCriteria(easy, hard []string) (map[string]any, []string) {
	criteria := map[string]any{
		"easy": smartEasyCriteria + " Pool members: " + strings.Join(easy, ", ") + ".",
		"hard": smartHardCriteria + " Pool members: " + strings.Join(hard, ", ") + ".",
	}
	return criteria, []string{"easy", "hard"}
}

// orderModelsByTier leads with the chosen tier's members in combo
// order, followed by the other tier's members in combo order — the
// full chain stays available as fallback, so a routing call can never
// make a request fail that the plain order would have served.
func orderModelsByTier(models []string, cfg db.ComboStrategy, tier string) []string {
	easy, hard := partitionByTier(models, cfg)
	if tier == "easy" {
		return append(easy, hard...)
	}
	return append(hard, easy...)
}

// parseSmartRouteAnswer reads the upstream answers.route object. A missing or
// empty choice is not a decision; confidence is optional.
func parseSmartRouteAnswer(raw []byte) *smartChoice {
	var payload struct {
		Answers struct {
			Route struct {
				Choice     string   `json:"choice"`
				Confidence *float64 `json:"confidence"`
			} `json:"route"`
		} `json:"answers"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	choice := strings.TrimSpace(payload.Answers.Route.Choice)
	if choice == "" {
		return nil
	}
	return &smartChoice{Choice: choice, Confidence: payload.Answers.Route.Confidence}
}

// isEasyTierModel reports whether a combo member belongs to the easy
// tier: listed in the combo's easyModels, or — when unlisted — matching
// the same name heuristics that describe it to the router as easy. The
// operator's hard list always wins over the heuristics.
func isEasyTierModel(modelStr string, cfg db.ComboStrategy) bool {
	for _, em := range cfg.EasyModels {
		if strings.TrimSpace(em) == modelStr {
			return true
		}
	}
	for _, hm := range cfg.HardModels {
		if strings.TrimSpace(hm) == modelStr {
			return false
		}
	}
	return describeModelForRouting(modelStr, false, false) == smartEasyCriteria
}

// demoteEasyTier applies the heavy-session guard to an ordered member
// list: in a heavy working session (see smartSessionInfo) no easy-tier
// member may lead the chain, because the router judged only the latest
// turn's wording and a heavy session's steering turns read as trivial.
// Easy members keep their relative order at the tail — they stay in
// the chain as fallback when the stronger members are down, so the
// guard can never make a request fail that would otherwise succeed.
// Light sessions and lists without a mix pass through unchanged.
func demoteEasyTier(models []string, cfg db.ComboStrategy, heavy bool) []string {
	if !heavy || len(models) <= 1 {
		return models
	}
	nonEasy := make([]string, 0, len(models))
	easy := make([]string, 0, len(models))
	changed := false
	seenEasy := false
	for _, m := range models {
		if isEasyTierModel(m, cfg) {
			easy = append(easy, m)
			seenEasy = true
		} else {
			if seenEasy {
				changed = true
			}
			nonEasy = append(nonEasy, m)
		}
	}
	if !changed || len(nonEasy) == 0 {
		return models
	}
	log.Info("combo", "smart-router heavy session: easy tier demoted", "easy", easy, "leading", nonEasy[0])
	return append(nonEasy, easy...)
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// getSmartStickyModel returns the session's pinned model when it is still
// live (TTL) and still a member of the combo. Upstream getStickyComboModel.
func (h *ChatHandler) getSmartStickyModel(comboName, sessionID string, models []string) string {
	if comboName == "" || sessionID == "" || len(models) == 0 {
		return ""
	}
	h.smartStickyMu.Lock()
	defer h.smartStickyMu.Unlock()
	entry, ok := h.smartSticky[smartStickyKey(comboName, sessionID)]
	if !ok {
		return ""
	}
	if time.Since(entry.LastUsed) > smartStickyTTL || !containsString(models, entry.Model) {
		delete(h.smartSticky, smartStickyKey(comboName, sessionID))
		return ""
	}
	entry.LastUsed = time.Now()
	h.smartSticky[smartStickyKey(comboName, sessionID)] = entry
	return entry.Model
}

// setSmartStickyModel pins a session's model. Upstream setStickyComboModel.
func (h *ChatHandler) setSmartStickyModel(comboName, sessionID, model string) {
	if comboName == "" || sessionID == "" || model == "" {
		return
	}
	h.pruneSmartSticky()
	h.smartStickyMu.Lock()
	defer h.smartStickyMu.Unlock()
	if h.smartSticky == nil {
		h.smartSticky = make(map[string]smartStickyEntry)
	}
	h.smartSticky[smartStickyKey(comboName, sessionID)] = smartStickyEntry{Model: model, LastUsed: time.Now()}
}

// clearSmartStickySession drops one session's pin, or every pin of a combo
// when sessionID is empty. Upstream clearStickyComboSession.
func (h *ChatHandler) clearSmartStickySession(comboName, sessionID string) {
	h.smartStickyMu.Lock()
	defer h.smartStickyMu.Unlock()
	if comboName == "" {
		h.smartSticky = nil
		return
	}
	if sessionID != "" {
		delete(h.smartSticky, smartStickyKey(comboName, sessionID))
		return
	}
	prefix := comboName + ":"
	for k := range h.smartSticky {
		if strings.HasPrefix(k, prefix) {
			delete(h.smartSticky, k)
		}
	}
}

// pruneSmartSticky bounds the store: expired entries go first, then the
// oldest quarter once the ceiling is still exceeded.
func (h *ChatHandler) pruneSmartSticky() {
	h.smartStickyMu.Lock()
	defer h.smartStickyMu.Unlock()
	if len(h.smartSticky) < smartMaxStickySessions {
		return
	}
	now := time.Now()
	for k, v := range h.smartSticky {
		if now.Sub(v.LastUsed) > smartStickyTTL {
			delete(h.smartSticky, k)
		}
	}
	if len(h.smartSticky) < smartMaxStickySessions {
		return
	}
	type aged struct {
		key string
		at  time.Time
	}
	entries := make([]aged, 0, len(h.smartSticky))
	for k, v := range h.smartSticky {
		entries = append(entries, aged{k, v.LastUsed})
	}
	// Selection by repeated min-scan: stable and allocation-light for a
	// once-per-5000-sessions prune.
	drop := len(entries) / 4
	for drop > 0 && len(entries) > 0 {
		oldest := 0
		for i := range entries {
			if entries[i].at.Before(entries[oldest].at) {
				oldest = i
			}
		}
		delete(h.smartSticky, entries[oldest].key)
		entries = append(entries[:oldest], entries[oldest+1:]...)
		drop--
	}
}

func smartStickyKey(comboName, sessionID string) string {
	return comboName + ":" + sessionID
}

// smartSwitchThreshold resolves the confidence floor for the configured
// policy (upstream handleComboChat's switchThreshold ladder).
func smartSwitchThreshold(cfg db.ComboStrategy) float64 {
	policy := strings.ToLower(strings.TrimSpace(cfg.SmartPolicy))
	threshold := 0.0
	if cfg.ConfidenceThreshold != nil {
		threshold = *cfg.ConfidenceThreshold
	}
	switch {
	case policy == "custom":
		if threshold > 0 {
			return threshold
		}
	case policy == "smart-switch-70":
		return 0.7
	case policy == "smart-switch-90":
		return 0.9
	case strings.HasPrefix(policy, "smart-switch") || policy == "smart" || policy == "":
		if threshold > 0 {
			return threshold
		}
	}
	return smartDefaultThreshold
}

// resolveSmartTier asks the configured System One model which tier
// this request belongs to. It never fails the turn: any problem
// (missing credentials, timeout, unparseable answer) returns nil and
// the strategy falls back to the combo's own ordering, as does a
// combo whose members all sit in one tier — there is nothing to
// decide there.
func (h *ChatHandler) resolveSmartTier(ctx context.Context, body []byte, models []string, cfg db.ComboStrategy) *smartChoice {
	if len(models) <= 1 {
		return nil
	}
	easy, hard := partitionByTier(models, cfg)
	if len(easy) == 0 || len(hard) == 0 {
		return nil
	}
	target := strings.TrimSpace(cfg.RouterModel)
	if target == "" {
		target = smartRouterDefaultModel
	}
	view, ok := ctx.Value(smartBodyViewKey{}).(smartBodyView)
	if !ok {
		view = viewSmartBody(body)
	}
	state := view.state
	if state == "" {
		return nil
	}
	criteria, choices := buildSmartTierCriteria(easy, hard)
	choice, err := h.querySystemOneRouter(ctx, target, state, criteria, choices)
	if err != nil || choice == nil {
		return nil
	}
	if choice.Choice != "easy" && choice.Choice != "hard" {
		return nil
	}
	return choice
}

// querySystemOneRouter performs the evaluation call against the router
// model's provider, using the same URL rules as the /v1/systemone lane.
func (h *ChatHandler) querySystemOneRouter(ctx context.Context, targetModel, state string, criteria map[string]any, choices []string) (*smartChoice, error) {
	modelInfo, err := h.resolveModel(targetModel)
	if err != nil || modelInfo == nil {
		log.Warn("combo", "smart-router: unrecognized router model", "model", targetModel)
		return nil, fmt.Errorf("unrecognized router model")
	}
	conn, connData, err := h.GetBestConnectionWithContext(ctx, modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
	if err != nil || connData == nil {
		// The default router model rides a NoAuth provider (free OpenCode);
		// it answers with synthetic credentials, like the /v1/systemone lane.
		cfg, known := providers.KnownProviders[modelInfo.Provider]
		if !known || (!cfg.NoAuth && cfg.DefaultAPIKey == "") {
			log.Warn("combo", "smart-router: no credentials for router provider", "provider", modelInfo.Provider)
			return nil, fmt.Errorf("no router credentials")
		}
		connData = &ConnectionData{ProxyPoolID: h.ResolveProviderProxyPoolID(modelInfo.Provider)}
	}
	apiKey := ExtractAPIKey(connData)
	connID := ""
	if conn != nil {
		connID = conn.ID
	}
	if refreshed, _, rErr := h.RefreshOAuthTokenIfExpired(connID, apiKey); rErr == nil && refreshed != "" {
		apiKey = refreshed
	}
	providerCfg, err := h.GetProviderConfig(modelInfo.Provider, connData)
	if err != nil || providerCfg == nil {
		return nil, fmt.Errorf("router provider config: %w", err)
	}
	if apiKey == "" && providerCfg.DefaultAPIKey != "" {
		apiKey = providerCfg.DefaultAPIKey
	}

	question := map[string]any{
		"model": modelInfo.Model,
		"state": state,
		"questions": map[string]any{
			"route": map[string]any{
				"type":         "choice",
				"instructions": "Which tier does this request belong to? Judge the request itself — its complexity, depth, and stakes — not the models.",
				"choices":      choices,
				"criteria":     criteria,
			},
		},
	}
	payload, err := json.Marshal(question)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, smartRouterTimeout)
	defer cancel()
	targetURL := systemoneTargetURL(providerCfg, connData.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	handlerutil.SetAuthHeader(req, apiKey, providerCfg.AuthHeader, providerCfg.AuthScheme)
	for k, v := range providerCfg.StaticHeaders {
		if req.Header.Get(k) == "" {
			req.Header.Set(k, v)
		}
	}
	client, clientErr := h.GetClientForConnection(connData)
	if clientErr != nil {
		return nil, clientErr
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Warn("combo", "smart-router: evaluation aborted", "error", err.Error())
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		log.Warn("combo", "smart-router: evaluation failed", "status", resp.StatusCode)
		return nil, fmt.Errorf("router status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return parseSmartRouteAnswer(raw), nil
}

// applySmartRouting orders a combo's models for the smart strategy and
// returns a context carrying the routing state the failure loop reads. Port
// of the smart branch of upstream handleComboChat: a pinned session model is
// reused (Strict Lock without an evaluation; Smart Switch re-evaluated and
// only switched above the confidence floor), and a fresh or per-turn session
// leads with the tier the System One evaluation picked. Every failure path
// keeps the combo's own ordering.
func (h *ChatHandler) applySmartRouting(ctx context.Context, body []byte, comboModels []string, comboName string) (context.Context, []string) {
	models := make([]string, len(comboModels))
	copy(models, comboModels)
	if len(models) <= 1 {
		return ctx, models
	}

	cfg := db.ComboStrategy{}
	if settings, err := h.Repo.GetSettings(); err == nil && settings != nil {
		if saved, ok := settings.ComboStrategies[comboName]; ok {
			cfg = saved
		}
	}
	policy := strings.ToLower(strings.TrimSpace(cfg.SmartPolicy))
	if policy == "" {
		policy = "smart"
	}
	sessionSticky := true
	if cfg.SessionSticky != nil {
		sessionSticky = *cfg.SessionSticky
	}
	isPerTurn := policy == "per-turn" || !sessionSticky
	isStrict := policy == "strict"
	threshold := smartSwitchThreshold(cfg)
	sessionID := handlerutil.GetSessionID(ctx)

	// One parse of the body feeds both the router's enriched state and
	// the heavy-session guard; the view rides the context so the
	// evaluation calls below reuse it instead of parsing again.
	view := viewSmartBody(body)
	ctx = context.WithValue(ctx, smartBodyViewKey{}, view)
	guard := func(ordered []string) []string {
		return demoteEasyTier(ordered, cfg, view.info.Heavy)
	}

	// A combo whose members all sit in one tier gives the router
	// nothing to decide: no evaluation, no decision record.
	if easyPool, hardPool := partitionByTier(models, cfg); len(easyPool) == 0 || len(hardPool) == 0 {
		return ctx, guard(models)
	}

	if view.info.Heavy {
		// Heavy working session: the tier is hard by rule, decided
		// locally without asking the router. The router only ever sees
		// the last few user turns, and in harness traffic those are
		// scaffolding — instruction blocks, environment updates, system
		// notifications — not the task, so its verdict on such a session
		// is noise (live audit, 2026-11-11: a 70K–383K-token agent
		// session pinned easy while the heavy-session guard below had
		// to undo the easy lead on every single turn). Lead with the
		// hard pool and pin its lead, so the pin, the record, and the
		// serving model all agree.
		ordered := orderModelsByTier(models, cfg, "hard")
		lead := ordered[0]
		if !isPerTurn && sessionID != "" {
			h.setSmartStickyModel(comboName, sessionID, lead)
		}
		log.Info("combo", "smart-router heavy session: hard by rule", "model", lead)
		dec := &smartDecision{Verdict: "hard", Source: "heavy-session", Model: lead, Threshold: threshold}
		return withSmartDecision(withSmartRouteState(ctx, &smartRouteState{comboName: comboName, sessionID: sessionID, activeModel: lead}), dec), guard(ordered)
	}

	var activeModel string
	if !isPerTurn && sessionID != "" {
		activeModel = h.getSmartStickyModel(comboName, sessionID, models)
	}

	pinFirst := func(model string) []string {
		ordered := make([]string, 0, len(models))
		ordered = append(ordered, model)
		for _, m := range models {
			if m != model {
				ordered = append(ordered, m)
			}
		}
		return ordered
	}

	if activeModel != "" && isStrict {
		// Strict Lock: reuse the pinned model without an evaluation.
		// The heavy-session guard still applies: a pin earned while the
		// session was light must not lead once the session turned heavy.
		log.Info("combo", "smart-router [strict] locked session", "session", sessionID, "model", activeModel)
		dec := &smartDecision{Verdict: smartTierOf(activeModel, cfg), Source: "strict", Model: activeModel, Threshold: threshold}
		return withSmartDecision(withSmartRouteState(ctx, &smartRouteState{comboName: comboName, sessionID: sessionID, activeModel: activeModel}), dec), guard(pinFirst(activeModel))
	}

	if activeModel != "" {
		// Smart Switch: re-evaluate the tier, and switch only above the floor.
		var evalChoice *smartChoice
		if choice := h.resolveSmartTier(ctx, body, models, cfg); choice != nil {
			evalChoice = choice
			conf := -1.0
			if choice.Confidence != nil {
				conf = *choice.Confidence
			}
			if choice.Choice != smartTierOf(activeModel, cfg) && conf >= threshold {
				ordered := orderModelsByTier(models, cfg, choice.Choice)
				lead := ordered[0]
				log.Info("combo", "smart-router [smart-switch] task changed", "from", activeModel, "to", lead, "tier", choice.Choice, "confidence", conf, "threshold", threshold)
				h.setSmartStickyModel(comboName, sessionID, lead)
				dec := &smartDecision{Verdict: choice.Choice, Source: "switch", Model: lead, Confidence: choice.Confidence, Threshold: threshold}
				return withSmartDecision(withSmartRouteState(ctx, &smartRouteState{comboName: comboName, sessionID: sessionID, activeModel: lead}), dec), guard(ordered)
			}
		}
		// Below the floor, on no decision, or on error: keep the warm cache.
		log.Info("combo", "smart-router [smart-switch] retained model", "model", activeModel)
		dec := &smartDecision{Verdict: smartTierOf(activeModel, cfg), Source: "retained", Model: activeModel, Threshold: threshold}
		if evalChoice != nil {
			dec.Confidence = evalChoice.Confidence
		}
		return withSmartDecision(withSmartRouteState(ctx, &smartRouteState{comboName: comboName, sessionID: sessionID, activeModel: activeModel}), dec), guard(pinFirst(activeModel))
	}

	// Turn 1 or per-turn: evaluate the tier and lead with its members.
	choice := h.resolveSmartTier(ctx, body, models, cfg)
	if choice == nil {
		log.Info("combo", "smart-router no decision, fallback to order", "combo", comboName)
		return withSmartDecision(ctx, &smartDecision{Verdict: "fallback", Source: "no-decision", Threshold: threshold}), guard(models)
	}
	conf := -1.0
	if choice.Confidence != nil {
		conf = *choice.Confidence
	}
	if threshold > 0 && conf >= 0 && conf < threshold {
		log.Info("combo", "smart-router confidence below threshold", "confidence", conf, "threshold", threshold)
		return withSmartDecision(ctx, &smartDecision{Verdict: "fallback", Source: "below-threshold", Confidence: choice.Confidence, Threshold: threshold}), guard(models)
	}
	ordered := orderModelsByTier(models, cfg, choice.Choice)
	lead := ordered[0]
	log.Info("combo", "smart-router selected", "tier", choice.Choice, "model", lead, "confidence", conf)
	if !isPerTurn && sessionID != "" {
		h.setSmartStickyModel(comboName, sessionID, lead)
	}
	dec := &smartDecision{Verdict: choice.Choice, Source: "selected", Model: lead, Confidence: choice.Confidence, Threshold: threshold}
	return withSmartDecision(withSmartRouteState(ctx, &smartRouteState{comboName: comboName, sessionID: sessionID, activeModel: lead}), dec), guard(ordered)
}

// withSmartRouteState attaches the smart routing state to the context.
func withSmartRouteState(ctx context.Context, state *smartRouteState) context.Context {
	return context.WithValue(ctx, smartRouteStateKey{}, state)
}

// clearFailedSmartSticky drops a session's pin when the model it was pinned to
// just failed, so the next turn re-evaluates instead of returning to a dead
// model. Upstream clears the pin at every combo failure site.
func (h *ChatHandler) clearFailedSmartSticky(ctx context.Context, failedModel string) {
	state, _ := ctx.Value(smartRouteStateKey{}).(*smartRouteState)
	if state == nil || state.sessionID == "" || state.activeModel == "" || failedModel != state.activeModel {
		return
	}
	h.clearSmartStickySession(state.comboName, state.sessionID)
}

// systemoneTargetURL mirrors the URL rules of the /v1/systemone media lane: a
// connection override or provider SystemoneURL wins, else the chat base URL's
// /chat/completions is swapped for /systemone.
func systemoneTargetURL(cfg *providers.ProviderConfig, connOverride string) string {
	override := strings.TrimSpace(connOverride)
	switch {
	case override != "" && strings.Contains(override, "/chat/completions"):
		return strings.Replace(strings.TrimRight(override, "/"), "/chat/completions", "/systemone", 1)
	case override != "":
		return buildSystemoneURLFromOverride(override, cfg)
	case cfg.SystemoneURL != "":
		return cfg.SystemoneURL
	default:
		base := strings.TrimRight(cfg.BaseURL, "/")
		if strings.Contains(base, "/chat/completions") {
			return strings.Replace(base, "/chat/completions", "/systemone", 1)
		}
		return base + "/systemone"
	}
}

func buildSystemoneURLFromOverride(override string, cfg *providers.ProviderConfig) string {
	if cfg.SystemonePath == "" {
		return strings.TrimRight(override, "/") + "/systemone"
	}
	return strings.TrimRight(override, "/") + cfg.SystemonePath
}
