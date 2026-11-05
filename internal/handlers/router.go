package handlers

import (
	"axonrouter/proxy/internal/constants"
	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/guardrails"
	"axonrouter/proxy/internal/handlers/batches"
	"axonrouter/proxy/internal/handlers/chat"
	"axonrouter/proxy/internal/handlers/dashboard"
	"axonrouter/proxy/internal/handlers/media"
	"axonrouter/proxy/internal/handlers/oauth"
	"axonrouter/proxy/internal/handlers/shared"
	"axonrouter/proxy/internal/handlers/sso"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/middleware"
	"axonrouter/proxy/internal/observ"
	"axonrouter/proxy/internal/routinglatency"
	"axonrouter/proxy/web"
	json "encoding/json/v2"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"time"
)

// Re-export TokenSaverConfig for root compatibility
type TokenSaverConfig = shared.TokenSaverConfig

// NewTokenSaverConfig re-exports shared.NewTokenSaverConfig.
func NewTokenSaverConfig(rtk, caveman, ponytail bool) *TokenSaverConfig {
	return shared.NewTokenSaverConfig(rtk, caveman, ponytail)
}

// SetupRoutes mounts all domain handlers on the provided router. It returns the
// engine's chat handler, which owns the semantic cache the engine actually
// reads and writes. The caller must pass that same handler to
// SetupDashboardRoutes: a second NewChatHandler builds a second
// PersistentStore over the same database with its own in-memory LRU, so the
// dashboard would report zero entries while the engine served hits, and
// clearing entries from the dashboard would leave the engine still serving them.
func SetupRoutes(r interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Head(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
	Put(pattern string, handlerFn http.HandlerFunc)
	Patch(pattern string, handlerFn http.HandlerFunc)
	Delete(pattern string, handlerFn http.HandlerFunc)
	HandleFunc(pattern string, handlerFn http.HandlerFunc)
}, repo *db.Repo, ts *TokenSaverConfig) *chat.ChatHandler {
	chatH := chat.NewChatHandler(repo, ts)
	mediaH := media.NewMediaHandler(repo, ts, chatH)
	oauthH := oauth.NewOAuthHandler(repo)

	dashH := dashboard.NewDashboardHandler(repo, ts)
	if chatH != nil {
		dashH.SemanticCache = chatH.SemanticCache
		// The resilience layer is owned by the data plane, because that is the
		// side that consults it on every request. The dashboard holds the same
		// instance so its panel reports the layer a request was judged
		// against, not a second one that can disagree with it.
		dashH.Resilience = chatH.Resilience
	}
	// Chat & Models Domain (no version here: the four version GETs are
	// public in SetupServerRouter, upstream PUBLIC_API_PATHS parity).
	r.Get("/changelog", chatH.HandleChangelog)
	r.Get("/api/changelog", chatH.HandleChangelog)
	r.Get("/models", chatH.HandleModels)
	r.Get("/models/info", chatH.HandleModelsInfo)
	r.Get("/models/{kind}", chatH.HandleModelsByKind)
	r.Get("/models/*", chatH.HandleModelLookup)
	r.Get("/v1/models", chatH.HandleModels)
	r.Get("/v1/models/*", chatH.HandleModelLookup)
	r.Get("/api/v1/models", chatH.HandleModels)
	r.Get("/api/v1/models/*", chatH.HandleModelLookup)
	// The bare /api/models is NOT registered here: it is a dashboard
	// route in Axon (the {models} catalogue shape batch 9 aligned),
	// and this group is the engine gate — a session caller was
	// refused while an engine key was admitted, the inverse of
	// Axon's caller class. It lives in SetupDashboardRoutes behind
	// the operator-class wrapper (council round 2, F-B3). The
	// wildcard below stays: it serves the engine-side model lookup.
	r.Get("/api/models/*", chatH.HandleModelLookup)
	// Catalog sync is an operator action, not an engine one (council
	// round 1, N-5): Axon's guard admits only a session, the CLI token,
	// or a login-off install to /api/* — an engine API key never reaches
	// this route there. These registrations sit in the RequireApiKey
	// group, so both verbs additionally take the operator-class wrapper
	// RequireOperatorCaller to land on Axon's caller class; the POST
	// triggers outbound catalog fetches and the GET discloses the sync
	// state, and Axon gates the pair identically.
	r.Get("/api/models/catalog-sync", RequireOperatorCaller(repo, chatH.HandleCatalogSyncStatus))
	r.Post("/api/models/catalog-sync", RequireOperatorCaller(repo, chatH.HandleCatalogSyncTrigger))
	// HEAD rides the same handlers as the model-catalogue GETs (and the
	// voices list below): Axon's Next.js serves HEAD from the GET
	// handler, and SDK health checks, load balancers and client
	// libraries probe with it — a 405 here reads as a dead gateway
	// (review cycle 2, N3). The net/http server discards the body of a
	// HEAD response itself, so the handler — and the Content-Length it
	// would have produced — is identical to GET by construction.
	r.Head("/models", chatH.HandleModels)
	r.Head("/models/info", chatH.HandleModelsInfo)
	r.Head("/models/{kind}", chatH.HandleModelsByKind)
	r.Head("/models/*", chatH.HandleModelLookup)
	r.Head("/v1/models", chatH.HandleModels)
	r.Head("/v1/models/*", chatH.HandleModelLookup)
	r.Head("/api/v1/models", chatH.HandleModels)
	r.Head("/api/v1/models/*", chatH.HandleModelLookup)
	r.Head("/api/models/*", chatH.HandleModelLookup)
	// Backend parity batch 6 — protocol surface (see
	// .changes/917-backend-parity-batch6-protocol-surface.md). Axon's
	// GET /v1 re-exports its models list; the /v1beta surface is the
	// Gemini-shaped catalogue + generateContent lane (chat package);
	// task/unified/video-generations are Axon-only engine routes. The
	// /api/v1 and /api/v1beta spellings reach these via the alias fold
	// in RequestLogger.
	r.Get("/v1", chatH.HandleModels)
	r.Head("/v1", chatH.HandleModels)
	r.Get("/v1beta/models", chatH.HandleV1BetaModels)
	r.Head("/v1beta/models", chatH.HandleV1BetaModels)
	r.Post("/v1beta/models/*", chatH.HandleV1BetaModelAction)
	r.Post("/task", mediaH.HandleTask)
	r.Post("/unified", HandleUnified)
	r.Post("/video/generations", HandleVideoGenerationsBaseline)
	r.Post("/chat/completions", routinglatency.Wrap("v1", chatH.HandleChatCompletions))
	r.Post("/messages", routinglatency.Wrap("v1", chatH.HandleMessages))
	r.Post("/messages/count_tokens", chatH.HandleCountTokens)
	r.Post("/api/chat", chatH.HandleOllamaChat)

	// Media, Audio, Video & Web Tools Domain
	r.Post("/embeddings", mediaH.HandleEmbeddings)
	r.Post("/rerank", mediaH.HandleRerank)
	// /v1/responses needs model resolution, combos, account fallback and usage
	// logging, none of which the media passthrough has, so it lives with the
	// chat handlers — the same place /v1/messages does.
	r.Post("/responses", routinglatency.Wrap("v1", chatH.HandleResponses))
	r.Post("/responses/compact", routinglatency.Wrap("v1", chatH.HandleResponsesCompact))
	// /codex/* is Axon's web-server mount for the same chat handler
	// (webServer.mjs app.all("/codex/*", chatHandler); chat.js's
	// pathname dispatch covers exactly the chat spellings) — council
	// round 3, F-1. The four spellings mirror the /v1 set above, same
	// handlers, same latency wrap; POST-only like the rest of the lane.
	r.Post("/codex/chat/completions", routinglatency.Wrap("v1", chatH.HandleChatCompletions))
	r.Post("/codex/messages", routinglatency.Wrap("v1", chatH.HandleMessages))
	r.Post("/codex/responses", routinglatency.Wrap("v1", chatH.HandleResponses))
	r.Post("/codex/api/chat", chatH.HandleOllamaChat)
	r.Post("/images/generations", mediaH.HandleImages)
	r.Post("/images/edits", mediaH.HandleImageEdits)
	r.Post("/audio/speech", mediaH.HandleAudioSpeech)
	r.Post("/audio/music", mediaH.HandleAudioMusic)
	r.Post("/v1/audio/music", mediaH.HandleAudioMusic)
	// Batch 9: the engine spelling gets Axon's reshaping wrapper
	// ({object:"list", data:[...]} + provider whitelist); the dashboard
	// spellings below keep the shared SPA-shaped handler.
	r.Get("/audio/voices", mediaH.HandleAudioVoicesV1)
	r.Head("/audio/voices", mediaH.HandleAudioVoicesV1)
	r.Get("/api/media-providers/tts/voices", mediaH.HandleAudioVoices)
	r.Get("/api/media-providers/tts/inworld/voices", mediaH.HandleAudioVoices)
	r.Get("/api/media-providers/tts/minimax/voices", mediaH.HandleAudioVoices)
	r.Get("/api/media-providers/tts/deepgram/voices", mediaH.HandleAudioVoices)
	r.Get("/api/media-providers/tts/elevenlabs/voices", mediaH.HandleAudioVoices)
	r.Post("/audio/transcriptions", mediaH.HandleAudioTranscriptions)
	r.Post("/audio/sound-generation", mediaH.HandleAudioSoundGeneration)
	r.Post("/v1/audio/sound-generation", mediaH.HandleAudioSoundGeneration)
	r.Post("/videos/generations", mediaH.HandleVideoGenerations)
	r.Post("/videos/edits", mediaH.HandleVideoEdits)
	r.Post("/videos/extensions", mediaH.HandleVideoExtensions)
	r.Get("/videos/{id}", mediaH.HandleVideoGet)
	r.Get("/videos/{id}/content", mediaH.HandleVideoContent)
	r.Post("/search", mediaH.HandleSearch)
	r.Post("/scrape", mediaH.HandleScrape)
	r.Post("/web/fetch", mediaH.HandleWebFetch)
	r.Post("/systemone", mediaH.HandleSystemone)

	// The relay-deploy endpoints live in the dashboard group below, mounted
	// under /api/proxy-pools/*-deploy behind RequireDashboardAuth (upstream
	// parity). They were registered here under RequireApiKey, so the dashboard
	// SPA — which authenticates with the session cookie and sends no engine
	// key — got 401 "Authentication required" from every Deploy button.

	// CLI Tools Status Domain (dashboard batch status for installed CLI tools)
	// The /api/cli-tools/all-statuses alias is registered in SetupServerRouter
	// under RequireDashboardAuth instead — it is a dashboard read. Registering it
	// here too would win the match under RequireApiKey and keep it unreachable
	// from the session cookie the SPA actually sends.
	r.Get("/cli-tools/all-statuses", media.NewCLIToolsHandler().HandleAllStatuses)

	// Headroom Management Domain (token-compression proxy lifecycle + dashboard proxy)
	headroomH := media.NewHeadroomHandler(repo)
	// The headroom write spellings are operator-only even on this
	// engine mount (council round 3, R3-S5): they reach pip-install
	// and process-spawn handlers, which no engine key may drive. The
	// dashboard-group copies are governed by the RequireOperatorForWrites
	// sweep; these shadowed copies are wrapped too, so a future mount
	// reorder cannot reopen the lane. Status/extras reads unchanged.
	r.Post("/headroom/start", RequireOperatorCaller(repo, headroomH.HandleHeadroomStart))
	r.Post("/headroom/stop", RequireOperatorCaller(repo, headroomH.HandleHeadroomStop))
	r.Post("/headroom/restart", RequireOperatorCaller(repo, headroomH.HandleHeadroomRestart))
	r.Get("/headroom/status", headroomH.HandleHeadroomStatus)
	r.Get("/headroom/extras", headroomH.HandleHeadroomExtras)
	r.Post("/headroom/extras", RequireOperatorCaller(repo, headroomH.HandleHeadroomExtras))
	r.Delete("/headroom/extras", RequireOperatorCaller(repo, headroomH.HandleHeadroomExtras))
	r.HandleFunc("/headroom/proxy", headroomH.HandleHeadroomProxy)
	r.HandleFunc("/headroom/proxy/*", headroomH.HandleHeadroomProxy)

	r.Post("/api/headroom/start", RequireOperatorCaller(repo, headroomH.HandleHeadroomStart))
	r.Post("/api/headroom/stop", RequireOperatorCaller(repo, headroomH.HandleHeadroomStop))
	r.Post("/api/headroom/restart", RequireOperatorCaller(repo, headroomH.HandleHeadroomRestart))
	r.Get("/api/headroom/status", headroomH.HandleHeadroomStatus)
	r.Get("/api/headroom/extras", headroomH.HandleHeadroomExtras)
	r.Post("/api/headroom/extras", RequireOperatorCaller(repo, headroomH.HandleHeadroomExtras))
	r.Delete("/api/headroom/extras", RequireOperatorCaller(repo, headroomH.HandleHeadroomExtras))
	r.HandleFunc("/api/headroom/proxy", headroomH.HandleHeadroomProxy)
	r.HandleFunc("/api/headroom/proxy/*", headroomH.HandleHeadroomProxy)
	// OAuth & Import Tokens Domain
	mountOAuthRoutes(r, repo, oauthH)

	// Usage Real-time SSE Stream & Stats Domain (dashboard topology animation + recent requests)
	r.Get("/usage/stream", HandleUsageStream(repo))
	r.Get("/api/usage/stream", HandleUsageStream(repo))
	r.Get("/usage/stats", HandleUsageStats(repo))
	r.Get("/api/usage/stats", HandleUsageStats(repo))
	r.Get("/api/combos/analytics", HandleCombosAnalytics(repo))
	r.Get("/api/usage/active-requests", HandleUsageActiveRequests(repo))
	r.Get("/api/usage/analytics", HandleUsageAnalytics(repo))
	r.Get("/api/usage/analytics/failures", HandleUsageAnalyticsFailures(repo))
	r.Get("/api/usage/history", HandleUsageHistory(repo))
	r.Get("/api/usage/logs", HandleUsageLogs(repo))
	r.Get("/api/usage/request-logs", HandleUsageRequestLogs(repo))
	r.Get("/api/usage/quotas", HandleUsageQuotas(repo))
	r.Get("/usage/chart", HandleUsageChart(repo))
	r.Get("/api/usage/chart", HandleUsageChart(repo))
	r.Get("/api/usage/dashboard", HandleUsageDashboard(repo))
	r.Get("/api/usage/request-details", HandleRequestDetails(repo))
	r.Get("/api/usage/export", HandleUsageExport(repo))
	r.Get("/api/usage/request-details/{id}", HandleRequestDetail(repo))
	r.Post("/api/usage/request-details/{id}/replay", HandleRequestDetailReplay(repo))
	r.Get("/api/usage/providers", dashH.HandleGetUsageProviders)
	r.Get("/api/usage/{connectionId}", dashH.HandleGetConnectionUsage)
	r.Get("/api/usage/{connectionId}/reset-credits", dashH.HandleListCodexResetCredits)
	r.Post("/api/usage/{connectionId}/reset-credits/consume", dashH.HandleConsumeCodexResetCredit)
	r.Get("/api/usage/{connectionId}/codex-reset-credits", dashH.HandleGetCodexResetCredits)
	r.Post("/api/usage/{connectionId}/codex-reset-credits", dashH.HandleConsumeCodexResetCredits)
	// GLM Reset Cards (Z.ai international)
	r.Get("/api/usage/{connectionId}/glm-reset-cards", dashH.HandleGetGLMResetCards)
	r.Post("/api/usage/{connectionId}/glm-reset-cards", dashH.HandleRedeemGLMResetCard)

	// Debug Tracing Domain (p50/p95 latency per provider+model)
	r.Get("/debug/traces", HandleDebugTraces)
	return chatH
}

// SetupDashboardRoutes mounts the dashboard REST API. It is wrapped in
// RequireDashboardAuth by the server router, which lets a cookie-authenticated
// browser session, a valid API key or the local CLI token through when login is
// enabled (upstream dashboardGuard).
func SetupDashboardRoutes(r chi.Router, repo *db.Repo, chatH *chat.ChatHandler) {
	var ts *TokenSaverConfig
	if chatH != nil {
		ts = chatH.TokenSaver
	}
	dashH := dashboard.NewDashboardHandler(repo, ts)
	if chatH != nil {
		dashH.SemanticCache = chatH.SemanticCache
		// The engine's handler holds the limiter the data plane meters provider
		// windows with, so the dashboard reports the same numbers the gate
		// enforces instead of its own private view of them.
		dashH.Limiter = chatH.RateLimiter
		// Same instance as the resilience panel reads: the data plane owns the
		// layer it consults on every request.
		dashH.Resilience = chatH.Resilience
	}
	ssoH := sso.NewHandler(repo)

	// Session probe (batch 7), moved into the dashboard group in
	// council round 3 (N-2 ALIGN): anonymous callers get the gate's
	// denial; session callers keep the full document.
	r.Get("/api/auth/session", HandleAuthSession)

	// Backend parity batch 6 — dashboard-side protocol families
	// (Axon: requireManagementAuth — local request or dashboard
	// session; the dashboard group is the Go analogue).
	r.Get("/api/protocols/status", HandleProtocolsStatus)
	r.Get("/api/protocols/a2a/handshake", HandleA2AHandshake)
	r.Post("/api/protocols/a2a/message", HandleA2AMessage)
	r.Get("/api/protocols/mcp/handshake", HandleMCPHandshake)
	r.Get("/api/protocols/mcp/tools", HandleMCPTools)
	r.Post("/api/protocols/mcp/invoke", HandleMCPInvoke)
	r.Get("/api/translator/load", HandleTranslatorLoad)
	r.Post("/api/translator/save", HandleTranslatorSave)
	r.Post("/api/translator/translate", HandleTranslatorTranslate)
	if chatH != nil {
		r.Post("/api/translator/send", chatH.HandleTranslatorSend)
	}
	// Backend parity batch 7 — credentials backup, MCP audit +
	// transports, system info. Credentials and MCP audit add upstream's
	// requireManagementAuth gate on top of the dashboard group gate; the
	// MCP HTTP transports carry their own client API-key gate, and their
	// tools are scope-gated exactly like upstream's (see
	// batch7_mcp_transport.go).
	// Council round 1 (S-1): all four routes additionally require an
	// operator-class caller. Export returns every provider credential
	// in plaintext and import replaces them; the audit pair shares the
	// gate and serves the tool-call record (input hashes, output
	// summaries, key ids). The management gate alone admits any
	// whitelisted source address, and the dashboard group admits any
	// engine API key — composed, a client key on the LAN was enough.
	r.Get("/api/credentials/export", RequireManagementAuth(repo, RequireOperatorCaller(repo, dashH.HandleCredentialsExport)))
	r.Post("/api/credentials/import", RequireManagementAuth(repo, RequireOperatorCaller(repo, dashH.HandleCredentialsImport)))
	r.Get("/api/mcp/audit", RequireManagementAuth(repo, RequireOperatorCaller(repo, HandleMCPAuditList(repo))))
	r.Get("/api/mcp/audit/stats", RequireManagementAuth(repo, RequireOperatorCaller(repo, HandleMCPAuditStats(repo))))
	r.Get("/api/mcp/sse", HandleMCPSSEStream(repo))
	r.Post("/api/mcp/sse", HandleMCPSSEMessage(repo))
	r.Post("/api/mcp/stream", HandleMCPStream(repo))
	r.Get("/api/system/machine-id", HandleSystemMachineID)
	r.Get("/api/system/memory", HandleSystemMemory(repo))
	r.Get("/api/system/version", HandleSystemVersion)

	r.Post("/api/morph/test-key", dashboard.HandleMorphTestKey(repo))
	r.Get("/api/morph/usage/stats", dashboard.HandleMorphUsageStats(repo))
	r.Get("/api/morph/usage/requests", dashboard.HandleMorphUsageRequests(repo))

	r.Get("/api/connections", dashH.HandleGetConnections)
	r.Get("/api/providers", dashH.HandleGetProvidersClient)
	r.Get("/api/providers/client", dashH.HandleGetProvidersClient)
	r.Post("/api/connections", dashH.HandleCreateConnection)
	r.Post("/api/providers/validate", dashH.HandleValidateProvider)
	// The provider registry the dashboard renders its catalog from. Registered here as
	// well as in dashboard.RegisterRoutes, because that table is mounted by tests
	// only: a route added there alone is reachable in a unit test and 404s on the
	// real server.
	r.Get("/api/providers/catalog", dashH.HandleGetProviderCatalog)
	r.Post("/api/providers/gemini-web/browser-login", dashH.HandleGeminiWebBrowserLogin)
	r.Post("/api/providers/gemini-web/import-chrome", dashH.HandleGeminiWebImportChrome)
	r.Post("/api/providers/deepseek-web/browser-login", dashH.HandleDeepSeekWebBrowserLogin)
	r.Post("/api/providers/deepseek-web/import-chrome", dashH.HandleDeepSeekWebImportChrome)
	r.Post("/api/connections/{id}/renew-deepseek", dashH.HandleDeepSeekWebRenew)
	r.Put("/api/connections/{id}", dashH.HandleUpdateConnection)
	r.Put("/api/providers/{id}", dashH.HandleUpdateConnection)
	r.Delete("/api/connections/{id}", dashH.HandleDeleteConnection)
	r.Delete("/api/providers/{id}", dashH.HandleDeleteConnection)
	r.Post("/api/connections/{id}/test", dashH.HandleTestConnection)
	r.Post("/api/connections/{id}/reorder", dashH.HandleReorderConnection)
	r.Post("/api/providers/{id}/reorder", dashH.HandleReorderConnection)
	r.Post("/api/providers/{id}/test", dashH.HandleTestConnection)
	r.Get("/api/providers/suggested-models", HandleSuggestedModels)
	// Backend parity batch 2 — providers family (AxonRouter contract ports,
	// see axon-backend-parity-map.md). Static segments win over the {id}
	// wildcard in chi, so stats/test-batch/bulk-*/auto-switch/kilo are safe
	// alongside /api/providers/{id}/...
	r.Get("/api/models/availability", dashH.HandleModelAvailability)
	r.Post("/api/models/availability", dashH.HandleClearModelCooldown)
	r.Get("/api/pricing", dashH.HandleGetPricing)
	r.Patch("/api/pricing", dashH.HandleUpdatePricing)
	r.Delete("/api/pricing", dashH.HandleResetPricing)
	r.Get("/api/provider-models", dashH.HandleProviderModels)
	r.Get("/api/providers/stats", dashH.HandleProviderStats)
	r.Post("/api/providers/test-batch", dashH.HandleTestBatch)
	r.Post("/api/providers/bulk-proxy", dashH.HandleBulkProxy)
	r.Post("/api/providers/bulk-reset-status", dashH.HandleBulkResetStatus)
	r.Post("/api/providers/noauth-sync", dashH.HandleNoauthSync)
	r.Get("/api/providers/kilo/free-models", dashH.HandleKiloFreeModels)
	r.Get("/api/providers/antigravity/auto-switch", dashH.HandleAntigravityAutoSwitchSettings)
	r.Put("/api/providers/antigravity/auto-switch", dashH.HandleAntigravityAutoSwitchSettings)
	r.Get("/api/providers/antigravity/auto-switch/active", dashH.HandleAntigravityAutoSwitchActive)
	r.Put("/api/providers/antigravity/auto-switch/active", dashH.HandleAntigravityAutoSwitchActive)
	r.Post("/api/providers/antigravity/auto-switch/rotate", dashH.HandleAntigravityAutoSwitchRotate)
	r.Get("/api/providers/codex/auto-switch", dashH.HandleCodexAutoSwitchSettings)
	r.Put("/api/providers/codex/auto-switch", dashH.HandleCodexAutoSwitchSettings)
	r.Get("/api/providers/codex/auto-switch/active", dashH.HandleCodexAutoSwitchActive)
	r.Put("/api/providers/codex/auto-switch/active", dashH.HandleCodexAutoSwitchActive)
	r.Post("/api/providers/codex/auto-switch/rotate", dashH.HandleCodexAutoSwitchRotate)
	r.Get("/api/providers/codex/instructions", dashH.HandleCodexInstructions)
	r.Put("/api/providers/codex/instructions", dashH.HandleCodexInstructions)
	r.Get("/api/providers/commandcode/instructions", dashH.HandleCommandCodeInstructions)
	r.Put("/api/providers/commandcode/instructions", dashH.HandleCommandCodeInstructions)
	r.Get("/api/providers/morph/instructions", dashH.HandleMorphInstructions)
	r.Put("/api/providers/morph/instructions", dashH.HandleMorphInstructions)
	r.Get("/api/providers/{id}/proxy-stats", dashH.HandleProviderProxyStats)
	r.Post("/api/providers/{id}/reset-status", dashH.HandleResetConnectionStatus)
	r.Post("/api/providers/{id}/unlock-model", dashH.HandleUnlockConnectionModel)
	r.Post("/api/providers/{id}/scan-free-models", dashH.HandleScanFreeModels)
	r.Post("/api/providers/{id}/sync-models", dashH.HandleSyncConnectionModels)
	r.Post("/api/providers/{id}/test-model", dashH.HandleTestConnectionModel)
	r.Post("/api/providers/{id}/test-models", dashH.HandleTestConnectionModels)
	// Usage & Quota Endpoints (dashboard quota tracker, usage stats, and topology stream)
	r.Get("/api/usage/stream", HandleUsageStream(repo))
	r.Get("/usage/stream", HandleUsageStream(repo))
	r.Get("/api/usage/stats", HandleUsageStats(repo))
	r.Get("/usage/stats", HandleUsageStats(repo))
	r.Get("/api/combos/analytics", HandleCombosAnalytics(repo))
	r.Get("/api/usage/active-requests", HandleUsageActiveRequests(repo))
	r.Get("/api/usage/analytics", HandleUsageAnalytics(repo))
	r.Get("/api/usage/analytics/failures", HandleUsageAnalyticsFailures(repo))
	r.Get("/api/usage/history", HandleUsageHistory(repo))
	r.Get("/api/usage/logs", HandleUsageLogs(repo))
	r.Get("/api/usage/request-logs", HandleUsageRequestLogs(repo))
	r.Get("/api/usage/quotas", HandleUsageQuotas(repo))
	r.Get("/api/usage/chart", HandleUsageChart(repo))
	r.Get("/api/usage/dashboard", HandleUsageDashboard(repo))
	r.Get("/api/usage/request-details", HandleRequestDetails(repo))
	r.Get("/api/usage/export", HandleUsageExport(repo))
	r.Get("/api/usage/request-details/{id}", HandleRequestDetail(repo))
	r.Post("/api/usage/request-details/{id}/replay", HandleRequestDetailReplay(repo))
	r.Get("/api/usage/providers", dashH.HandleGetUsageProviders)
	r.Get("/api/usage/{connectionId}", dashH.HandleGetConnectionUsage)

	r.Get("/api/usage/{connectionId}/reset-credits", dashH.HandleListCodexResetCredits)
	r.Post("/api/usage/{connectionId}/reset-credits/consume", dashH.HandleConsumeCodexResetCredit)
	r.Get("/api/usage/{connectionId}/codex-reset-credits", dashH.HandleGetCodexResetCredits)
	r.Post("/api/usage/{connectionId}/codex-reset-credits", dashH.HandleConsumeCodexResetCredits)
	// GLM Reset Cards (Z.ai international)
	r.Get("/api/usage/{connectionId}/glm-reset-cards", dashH.HandleGetGLMResetCards)
	r.Post("/api/usage/{connectionId}/glm-reset-cards", dashH.HandleRedeemGLMResetCard)

	// Cache Analytics & Management
	r.Get("/api/cache", dashH.HandleGetCache)
	r.Delete("/api/cache", dashH.HandleDeleteCache)
	r.Get("/api/cache/entries", dashH.HandleGetCacheEntries)
	r.Delete("/api/cache/entries", dashH.HandleDeleteCacheEntry)

	// Compression Analytics
	r.Get("/api/analytics/compression", dashH.HandleGetCompressionAnalytics)

	// Jailbreak: agent prompt presets + per-model binding. Registered here and in
	// dashboard.RegisterRoutes (a tests-only table); a route added there alone
	// 404s on the real server — the same trap as the per-node limits pair.
	r.Get("/api/jailbreak", dashH.HandleGetJailbreak)
	r.Put("/api/jailbreak", dashH.HandlePutJailbreak)
	r.Get("/api/jailbreak/presets", dashH.HandleListJailbreakPresets)
	r.Put("/api/jailbreak/presets", dashH.HandlePutJailbreakPreset)
	r.Delete("/api/jailbreak/presets/{id}", dashH.HandleDeleteJailbreakPreset)
	r.Post("/api/jailbreak/presets/{id}/duplicate", dashH.HandleDuplicateJailbreakPreset)
	r.Get("/api/jailbreak/models", dashH.HandleListJailbreakModels)
	r.Get("/api/jailbreak/models/catalog", dashH.HandleJailbreakModelCatalog)
	r.Put("/api/jailbreak/models", dashH.HandleSetJailbreakModel)
	r.Delete("/api/jailbreak/models", dashH.HandleDeleteJailbreakModel)
	r.Get("/api/jailbreak/excludes", dashH.HandleGetJailbreakExcludes)
	r.Put("/api/jailbreak/excludes", dashH.HandlePutJailbreakExcludes)
	r.Post("/api/jailbreak/import", dashH.HandleImportJailbreak)
	r.Post("/api/jailbreak/resolve", dashH.HandleExplainJailbreak)
	r.Get("/api/jailbreak/usage", dashH.HandleJailbreakUsage)
	r.Post("/api/jailbreak/preview", dashH.HandlePreviewJailbreak)
	r.Put("/api/jailbreak/presets/order", dashH.HandleReorderJailbreakPresets)
	r.Post("/api/jailbreak/presets/upload", dashH.HandleUploadJailbreakPreset)

	r.Get("/api/provider-nodes", dashH.HandleGetProviderNodes)
	r.Post("/api/provider-nodes", dashH.HandleCreateProviderNode)
	r.Put("/api/provider-nodes/{id}", dashH.HandleUpdateProviderNode)
	r.Delete("/api/provider-nodes/{id}", dashH.HandleDeleteProviderNode)
	r.Post("/api/provider-nodes/validate", dashH.HandleValidateProviderNode)
	// Per-node capacity limits (docs/provider-limits-plan.md). These live here as
	// well as in dashboard.RegisterRoutes because that table is mounted by tests
	// only: a route added there alone is reachable in a unit test and 404s on the
	// real server, which is how this pair was nearly shipped.
	r.Put("/api/provider-nodes/{id}/limits", dashH.HandleUpdateProviderNodeLimits)
	r.Post("/api/provider-nodes/{id}/quota/reset", dashH.HandleResetProviderNodeQuota)
	r.Get("/api/providers/{id}/models", dashH.HandleGetConnectionModels)
	// export-keys returns PLAINTEXT provider credentials — the one
	// sensitive read the round-3 census found still key-reachable
	// (R3-S2); it takes the operator wrap individually since the
	// write sweep below does not touch GET classes.
	r.Get("/api/providers/{id}/export-keys", RequireOperatorCaller(repo, dashH.HandleExportProviderKeys))
	// Bulk removal of a node's imported models; the single-row form stays at
	// DELETE /api/models/custom/{key}.
	r.Delete("/api/providers/{id}/models", dashH.HandleDeleteProviderModels)
	r.Get("/api/providers/{id}/overrides", dashH.HandleGetProviderOverrides)
	r.Put("/api/providers/{id}/overrides", dashH.HandleSaveProviderOverrides)

	r.Get("/api/combos", dashH.HandleGetCombos)
	r.Post("/api/combos", dashH.HandleCreateCombo)
	r.Post("/api/combos/auto-free", dashH.HandleAutoFreeCombo)
	r.Post("/api/combos/auto-family", dashH.HandleAutoFamilyCombos)
	r.Get("/api/combos/presets", dashH.HandleGetComboPresets)
	r.Post("/api/combos/presets", dashH.HandleCreateComboPresets)
	r.Get("/api/combos/hidden", dashH.HandleGetHiddenCombos)
	r.Post("/api/combos/hidden", dashH.HandleHideCombos)
	r.Delete("/api/combos/hidden", dashH.HandleUnhideCombos)
	r.Post("/api/combos/reorder", dashH.HandleReorderCombos)
	r.Post("/api/combos/test", dashH.HandleTestCombo)
	r.Put("/api/combos/{id}", dashH.HandleUpdateCombo)
	// Smart select combo: dry-run a rule list, and apply every combo's rules on
	// demand. These live here as well as in dashboard.RegisterRoutes for the
	// same reason the node-limit pair does — that table is mounted by tests
	// only, so a route registered there alone answers 200 in a unit test and
	// "404 page not found" on the real server, which is exactly how the
	// smart-rules editor shipped.
	r.Post("/api/combos/reconcile", dashH.HandleReconcileSmartCombos)
	r.Post("/api/combos/{id}/smart/preview", dashH.HandleComboSmartPreview)
	r.Post("/api/combos/{id}/smart/load", dashH.HandleComboSmartLoad)
	r.Get("/api/models/meta", dashH.HandleGetModelMeta)
	r.Delete("/api/combos/{id}", dashH.HandleDeleteCombo)

	r.Get("/api/proxy-pools", dashH.HandleGetProxyPools)
	r.Post("/api/proxy-pools", dashH.HandleCreateProxyPool)
	r.Put("/api/proxy-pools/{id}", dashH.HandleUpdateProxyPool)
	r.Delete("/api/proxy-pools/{id}", dashH.HandleDeleteProxyPool)
	r.Post("/api/proxy-pools/{id}/test", dashH.HandleTestProxyPool)

	// Relay deploy endpoints. Dashboard reads/writes, not engine traffic: the
	// SPA reaches them with its session cookie, exactly like the pool CRUD
	// above. chi prefers the static segment over {id}, so these four cannot
	// be shadowed by the parameterised routes registered first.
	relayH := media.NewMediaHandler(repo, nil, nil)
	r.Post("/api/proxy-pools/vercel-deploy", relayH.HandleVercelDeploy)
	r.Post("/api/proxy-pools/deno-deploy", relayH.HandleDenoDeploy)
	r.Post("/api/proxy-pools/cloudflare-deploy", relayH.HandleCloudflareDeploy)
	r.Post("/api/proxy-pools/netlify-deploy", relayH.HandleNetlifyDeploy)

	// Backend parity batch 4 — proxy infra family (AxonRouter contract
	// ports, see .changes/915). Static segments (fitness, health-check,
	// recover, verify-subscription) win over {id} in chi, same as the
	// relay-deploy routes above.
	r.Get("/api/proxy-pools/fitness", dashH.HandleGetProxyPoolsFitness)
	r.Post("/api/proxy-pools/fitness/clear-all", dashH.HandleClearProxyPoolsFitness)
	r.Post("/api/proxy-pools/{id}/fitness/clear", dashH.HandleClearProxyPoolFitness)
	r.Post("/api/proxy-pools/{id}/geo-probe", dashH.HandleGeoProbeProxyPool)
	r.Get("/api/proxy-pools/health-check", dashH.HandleGetProxyPoolsHealthCheck)
	r.Post("/api/proxy-pools/health-check", dashH.HandleProxyPoolsHealthCheck)
	r.Post("/api/proxy-pools/recover", dashH.HandleRecoverProxyPools)
	r.Get("/api/proxy-groups", dashH.HandleGetProxyGroups)
	r.Post("/api/proxy-groups", dashH.HandleCreateProxyGroup)
	r.Post("/api/proxy-groups/verify-subscription", dashH.HandleVerifySubscription)
	r.Get("/api/proxy-groups/{id}", dashH.HandleGetProxyGroup)
	r.Put("/api/proxy-groups/{id}", dashH.HandleUpdateProxyGroup)
	r.Delete("/api/proxy-groups/{id}", dashH.HandleDeleteProxyGroup)
	r.Post("/api/proxy-groups/{id}/remove-all", dashH.HandleRemoveAllProxyGroup)
	r.Post("/api/proxy-groups/{id}/sync", dashH.HandleSyncProxyGroup)
	r.Get("/api/circuit-breakers", dashH.HandleGetCircuitBreakers)
	r.Delete("/api/circuit-breakers", dashH.HandleResetCircuitBreakers)
	r.Get("/api/routing/latency", dashH.HandleGetRoutingLatency)
	r.Get("/api/keys", dashH.HandleGetApiKeys)
	r.Post("/api/keys", dashH.HandleCreateApiKey)
	r.Delete("/api/keys/{id}", dashH.HandleDeleteApiKey)
	r.Put("/api/keys/{id}/toggle", dashH.HandleToggleApiKey)
	r.Post("/api/keys/{id}/rotate", dashH.HandleRotateApiKey)
	r.Post("/api/keys/{id}/reset-quota", dashH.HandleResetApiKeyQuota)
	r.Get("/api/keys/{id}/models", dashH.HandleGetApiKeyModels)
	r.Put("/api/keys/{id}/models", dashH.HandleSetApiKeyModels)
	r.Put("/api/keys/{id}", dashH.HandleUpdateApiKey)
	r.Get("/api/plans", dashH.HandleListPlans)
	r.Post("/api/plans", dashH.HandleCreatePlan)
	r.Get("/api/plans/{id}", dashH.HandleGetPlan)
	r.Put("/api/plans/{id}", dashH.HandleUpdatePlan)
	r.Delete("/api/plans/{id}", dashH.HandleDeletePlan)
	r.Get("/api/plans/{id}/keys", dashH.HandlePlanKeys)
	r.Get("/api/metrics", observ.Handler().ServeHTTP)
	r.Get("/api/vault/status", dashH.HandleGetVaultStatus)
	r.Get("/api/guardrails/policies", dashH.HandleGetGuardrailPolicies)
	r.Post("/api/guardrails/policies", dashH.HandleCreateGuardrailPolicy)
	r.Put("/api/guardrails/policies/{id}", dashH.HandleUpdateGuardrailPolicy)
	r.Delete("/api/guardrails/policies/{id}", dashH.HandleDeleteGuardrailPolicy)
	r.Get("/api/guardrails/logs", dashH.HandleListGuardrailLogs)
	// Rotation takes the new master key from the request body and
	// re-wraps every connection DEK under it: in the wrong caller's
	// hands it irreversibly destroys the sealed credential store,
	// so it is operator-class like the other host-secret operations
	// (council round 2, R2-S2).
	r.Post("/api/vault/rotate", RequireOperatorCaller(repo, dashH.HandleRotateVault))
	// The bare /api/models list is Axon's dashboard catalogue route:
	// it serves the {models} shape (HandleModels branches on the
	// path) behind the dashboard guard, where an engine API key is
	// not a credential. It was registered only in the engine group,
	// which inverted the caller class (council round 2, F-B3), so it
	// is registered here — and, like catalog-sync in round 1, takes
	// RequireOperatorCaller so the dashboard gate's key admission
	// does not widen it: session, CLI token, or a login-off install
	// only. HEAD rides the GET handler, as in the engine group.
	r.Get("/api/models", RequireOperatorCaller(repo, chatH.HandleModels))
	r.Head("/api/models", RequireOperatorCaller(repo, chatH.HandleModels))
	r.Get("/api/models/custom", dashH.HandleGetCustomModels)
	r.Get("/api/models/caps", dashH.HandleGetModelCaps)
	r.Post("/api/models/custom", dashH.HandleSaveCustomModel)
	r.Delete("/api/models/custom/{key}", dashH.HandleDeleteCustomModel)
	r.Get("/api/models/disabled", dashH.HandleGetDisabledModels)
	// mediaH needs the chat handler every chat-lane probe is forwarded through,
	// so it is built with chatH — not the nil-chat relay handler below, whose
	// probes would panic instead of reaching the provider.
	mediaH := media.NewMediaHandler(repo, nil, chatH)
	r.Post("/api/models/test", mediaH.HandleTestModel)
	r.Post("/api/combos/{id}/test-models", mediaH.HandleComboTestModels)
	r.Get("/api/media-providers/tts/voices", mediaH.HandleAudioVoices)
	r.Get("/api/media-providers/tts/inworld/voices", mediaH.HandleAudioVoices)
	r.Get("/api/media-providers/tts/minimax/voices", mediaH.HandleAudioVoices)
	r.Get("/api/media-providers/tts/deepgram/voices", mediaH.HandleAudioVoices)
	r.Get("/api/media-providers/tts/elevenlabs/voices", mediaH.HandleAudioVoices)
	r.Put("/api/models/disabled/{provider}", dashH.HandleSaveDisabledModels)
	r.Post("/api/models/auto-disable", dashH.HandleAutoDisableModels)
	r.Get("/api/models/health", dashH.HandleGetModelHealth)
	r.Put("/api/models/health", dashH.HandleSaveModelHealth)
	r.Get("/api/models/alias", dashH.HandleGetModelAliases)
	r.Get("/api/models/deprecations", dashH.HandleGetModelDeprecations)
	r.Post("/api/models/sync", dashH.HandleSyncProviderModels)
	r.Put("/api/models/alias", dashH.HandleSetModelAlias)
	r.Delete("/api/models/alias", dashH.HandleDeleteModelAlias)

	r.Get("/api/settings", dashH.HandleGetSettings)
	// The settings write is operator-class (council round 2, R2-S1):
	// the generic update merges caller JSON into the settings row,
	// which carries the security configuration itself (requireLogin,
	// ipWhitelist, trustedProxyEnabled) — an engine API key admitted
	// by the dashboard gate could rewrite those and collapse every
	// gate that composes on them. The read stays on the plain gate;
	// round 1's S-3 strips the secrets from what it serves.
	r.Put("/api/settings", RequireOperatorCaller(repo, dashH.HandleUpdateSettings))
	r.Patch("/api/settings", RequireOperatorCaller(repo, dashH.HandleUpdateSettings))
	r.Put("/settings", RequireOperatorCaller(repo, dashH.HandleUpdateSettings))
	r.Patch("/settings", RequireOperatorCaller(repo, dashH.HandleUpdateSettings))

	// Settings backup/restore + outbound proxy diagnostics (profile page)
	r.Get("/api/settings/database", dashH.HandleExportDatabase)
	r.Post("/api/settings/database", dashH.HandleImportDatabase)
	r.Get("/api/settings/database/stats", dashH.HandleGetDatabaseStats)
	r.Post("/api/settings/database/prune", dashH.HandlePruneDatabase)
	r.Post("/api/settings/database/prune-usage", dashH.HandlePruneUsageHistory)
	r.Post("/api/settings/database/vacuum", dashH.HandleVacuumDatabase)
	r.Post("/api/settings/proxy-test", dashH.HandleProxyTest)
	// White-label logo upload: the file lands under DATA_DIR/uploads and
	// appLogoUrl is rewritten to the path this gateway serves it from.
	r.Post("/api/settings/logo", dashH.HandleUploadLogo)

	// Currency rates (profile page): reads live USD rates for the catalog codes
	// and stores the subset the display uses.
	r.Post("/api/settings/currency/refresh", dashH.HandleRefreshCurrencyRates)

	// Token saver testing benches & filter catalog
	r.Get("/api/tokensaver/rtk/filters", dashH.HandleGetRTKFilters)
	r.Post("/api/tokensaver/rtk/test", dashH.HandleTestRTK)
	r.Post("/api/tokensaver/caveman/test", dashH.HandleTestCaveman)

	// Headroom token-compression proxy management (dashboard parity)
	headroomH := media.NewHeadroomHandler(repo)
	r.Get("/api/headroom/status", headroomH.HandleHeadroomStatus)
	r.Post("/api/headroom/start", headroomH.HandleHeadroomStart)
	r.Post("/api/headroom/stop", headroomH.HandleHeadroomStop)
	r.Post("/api/headroom/restart", headroomH.HandleHeadroomRestart)
	r.Get("/api/headroom/extras", headroomH.HandleHeadroomExtras)
	r.Post("/api/headroom/extras", headroomH.HandleHeadroomExtras)
	r.Delete("/api/headroom/extras", headroomH.HandleHeadroomExtras)
	r.HandleFunc("/api/headroom/proxy", headroomH.HandleHeadroomProxy)
	r.HandleFunc("/api/headroom/proxy/*", headroomH.HandleHeadroomProxy)

	// Tunnel & Tailscale
	r.Get("/api/tunnel/status", dashH.HandleTunnelStatus)
	r.Post("/api/tunnel/enable", dashH.HandleTunnelEnable)
	r.Post("/api/tunnel/disable", dashH.HandleTunnelDisable)
	r.Get("/api/tunnel/tailscale-check", dashH.HandleTailscaleCheck)
	r.Post("/api/tunnel/tailscale-enable", dashH.HandleTailscaleEnable)
	r.Post("/api/tunnel/tailscale-disable", dashH.HandleTailscaleDisable)

	// Single Sign-On settings checks + SP metadata (profile page)
	r.Post("/api/auth/saml/test", ssoH.HandleSamlTest)
	r.Get("/api/auth/saml/metadata", ssoH.HandleSamlMetadata)

	// Mount OAuth routes under dashboard auth so web dashboard can initiate and exchange tokens
	oauthH := oauth.NewOAuthHandler(repo)
	mountOAuthRoutes(r, repo, oauthH)
}

// SetupConsoleLogRoutes mounts operational log APIs behind a full dashboard
// session or local CLI token. Client API keys are intentionally rejected.
func SetupConsoleLogRoutes(r chi.Router, repo *db.Repo) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireConsoleLogAuth(repo))
		r.Get("/api/translator/console-logs", HandleConsoleLogsGet)
		r.Delete("/api/translator/console-logs", HandleConsoleLogsDelete)
		r.Get("/api/translator/console-logs/stream", HandleConsoleLogsStream)
		r.Get("/api/translator/console-logs/level", HandleConsoleLogsLevelGet)
		r.Put("/api/translator/console-logs/level", HandleConsoleLogsLevelPut)
		// Batch 7: upstream's top-level /api/console-logs is the canonical
		// route and the translator mount is its re-export; same handlers,
		// same auth, no second log machinery.
		r.Get("/api/console-logs", HandleConsoleLogsGet)
		r.Delete("/api/console-logs", HandleConsoleLogsDelete)
		r.Get("/api/console-logs/stream", HandleConsoleLogsStream)
	})
}

func mountOAuthRoutes(r interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
}, repo *db.Repo, oauthH *oauth.OAuthHandler) {
	r.Post("/api/oauth/{provider}/import", oauthH.HandleOAuthImport)
	r.Get("/api/oauth/kiro/social-authorize", oauthH.HandleOAuthKiroSocialAuthorize)
	r.Post("/api/oauth/kiro/social-exchange", oauthH.HandleOAuthKiroSocialExchange)
	r.Post("/api/oauth/kiro/import", oauthH.HandleKiroImport)
	r.Post("/api/oauth/kiro/import-cli-proxy", oauthH.HandleKiroImportCliProxy)
	// The auto-imports read host credential stores, so Axon classes
	// them LOCAL_ONLY on top of ALWAYS_PROTECTED (dashboardGuard.js);
	// the locality wrapper is RequireLocalOnlyRoute (council round 2,
	// F-B2).
	r.Get("/api/oauth/kiro/auto-import", RequireLocalOnlyRoute(repo, oauthH.HandleKiroAutoImport))
	r.Post("/api/oauth/kiro/api-key", oauthH.HandleKiroAPIKey)
	r.Post("/api/oauth/codex/bulk-import", oauthH.HandleOAuthCodexBulkImport)
	r.Post("/api/oauth/grok-cli/bulk-import", oauthH.HandleOAuthGrokCliBulkImport)
	r.Post("/api/oauth/freebuff/initiate", oauthH.HandleFreebuffInitiate)
	r.Post("/api/oauth/freebuff/poll", oauthH.HandleFreebuffPoll)
	r.Get("/api/oauth/freebuff/session", oauthH.HandleFreebuffSessionStatus)
	r.Post("/api/oauth/freebuff/session/switch", oauthH.HandleFreebuffSessionSwitch)
	r.Get("/api/oauth/antigravity/authorize", oauthH.HandleAntigravityAuthorize)
	r.Post("/api/oauth/antigravity/exchange", oauthH.HandleAntigravityExchange)
	r.Get("/api/oauth/cline/authorize", oauthH.HandleClineAuthorize)
	r.Post("/api/oauth/cline/exchange", oauthH.HandleClineExchange)
	r.Get("/api/oauth/pkce/authorize", oauthH.HandlePKCEAuthorize)
	r.Post("/api/oauth/pkce/exchange", oauthH.HandlePKCEExchange)
	// Codex's OAuth client only accepts its registered loopback redirect URI,
	// so the callback is served by a dedicated fixed-port listener rather than
	// the dashboard's /callback page. See internal/handlers/oauth/codex_proxy.go.
	r.Get("/api/oauth/codex/start-proxy", oauthH.HandleCodexStartProxy)
	r.Get("/api/oauth/codex/poll-status", oauthH.HandleCodexPollStatus)
	r.Get("/api/oauth/codex/stop-proxy", oauthH.HandleCodexStopProxy)
	r.Get("/api/oauth/authcode/authorize", oauthH.HandleAuthCodeAuthorize)
	r.Post("/api/oauth/authcode/exchange", oauthH.HandleAuthCodeExchange)
	r.Get("/api/oauth/trae/authorize", oauthH.HandleTraeAuthorize)
	r.Post("/api/oauth/trae/exchange", oauthH.HandleTraeExchange)
	r.Get("/api/oauth/windsurf/authorize", oauthH.HandleWindsurfAuthorize)
	r.Post("/api/oauth/windsurf/exchange", oauthH.HandleWindsurfExchange)
	r.Get("/api/oauth/zed/authorize", oauthH.HandleZedAuthorize)
	r.Post("/api/oauth/zed/exchange", oauthH.HandleZedExchange)
	r.Post("/api/oauth/device/start", oauthH.HandleDeviceStart)
	r.Post("/api/oauth/device/poll", oauthH.HandleDevicePoll)
	r.Post("/api/oauth/cursor/import", oauthH.HandleCursorImport)
	r.Get("/api/oauth/cursor/auto-import", RequireLocalOnlyRoute(repo, oauthH.HandleCursorAutoImport))
	r.Get("/api/oauth/kimchi/authorize", oauthH.HandleKimchiAuthorize)
	r.Post("/api/oauth/kimchi/exchange", oauthH.HandleKimchiExchange)
	r.Post("/api/oauth/gitlab/pat", oauthH.HandleGitlabPAT)
	r.Post("/api/oauth/iflow/cookie", oauthH.HandleIflowCookie)
	r.Get("/api/oauth/xiaomi-mimo/authorize", oauthH.HandleMimoAuthorize)
	r.Post("/api/oauth/xiaomi-mimo/exchange", oauthH.HandleMimoExchange)
	// Batch 3 (AxonRouter parity): credential import / auto-import routes
	// and the generic {provider}/{action} dispatch. Chi resolves static
	// segments ahead of {provider}/{action}, and the generic route only
	// sees actions no dedicated route claims.
	r.Post("/api/oauth/codex/import-token", oauthH.HandleCodexImportToken)
	r.Post("/api/oauth/freebuff/import", oauthH.HandleFreebuffImport)
	r.Get("/api/oauth/freebuff/auto-import", oauthH.HandleFreebuffAutoImport)
	r.Post("/api/oauth/freebuff/realtest", oauthH.HandleFreebuffRealtest)
	r.Post("/api/oauth/supergrok/import", oauthH.HandleSupergrokImport)
	r.Post("/api/oauth/xiaomi-mimo/api-key", oauthH.HandleMimoAPIKey)
	r.Get("/api/oauth/xiaomi-mimo/auto-import", oauthH.HandleMimoAutoImport)
	r.Post("/api/oauth/zed/import", oauthH.HandleZedImport)
	r.Get("/api/oauth/zed/auto-import", RequireLocalOnlyRoute(repo, oauthH.HandleZedAutoImport))
	r.Get("/api/oauth/{provider}/{action}", oauthH.HandleOAuthDynamicGet)
	r.Post("/api/oauth/{provider}/{action}", oauthH.HandleOAuthDynamicPost)
}

// operatorWriteExempt names the dashboard-group writes that stay
// reachable for engine-key callers under the round-3 operator sweep
// (RequireOperatorForWrites). Every entry is a POST whose effect is
// either gated again inside the handler chain or confined to the
// caller's own engine domain; everything not listed here requires an
// operator-class caller. The list:
//
//   - /api/mcp/sse, /api/mcp/stream — the MCP transports. External MCP
//     clients authenticate with an engine API key BY DESIGN; the
//     handlers enforce their own key validity + scope gate
//     (batch 7), and the tools they expose are the key's own domain.
//   - /api/protocols/mcp/invoke — forwards the caller's own
//     Authorization / X-Api-Key into its self-call; the MCP surface
//     it reaches enforces the same key + scope gate.
//   - /api/protocols/a2a/message — a zero-effect acknowledgement
//     echo, and load-bearing as one: the protocols status probe
//     self-POSTs to it over loopback with no credentials, so gating
//     it would break the status surface.
//   - /api/translator/send — a chat completion: LLM spend in the
//     key's own domain, no administrative effect.
//   - The OAuth COMPLETION family under /api/oauth/ — a final path
//     segment of "exchange" (incl. the "-exchange" spellings),
//     "initiate", and the device/freebuff poll+start posts.
//     Completing a flow requires the provider-issued code the caller
//     obtained interactively, and the resulting connection is the
//     engine's own account pool. The administrative OAuth writes
//     (import / bulk-import / import-token / api-key / pat / cookie /
//     token-switch / dynamic actions) are NOT exempt and are gated.
func operatorWriteExempt(method, path string) bool {
	if method != http.MethodPost {
		return false
	}
	switch path {
	case "/api/mcp/sse", "/api/mcp/stream",
		"/api/protocols/mcp/invoke", "/api/protocols/a2a/message",
		"/api/translator/send",
		"/api/oauth/device/start", "/api/oauth/device/poll",
		"/api/oauth/freebuff/poll":
		return true
	}
	if strings.HasPrefix(path, "/api/oauth/") {
		last := path[strings.LastIndex(path, "/")+1:]
		if last == "exchange" || last == "initiate" || strings.HasSuffix(last, "-exchange") {
			return true
		}
	}
	return false
}

// SetupServerRouter mounts public endpoints (/health, /api/hello) and
// API-key protected routes (all engine + admin routes) on the chi router.
//
// It returns the engine's chat handler: the one SetupRoutes built and the
// dashboard group is mounted on. The auto-ping scheduler sends its pings
// through that same instance, so a ping resolves credentials, refreshes OAuth
// and picks an executor the way real traffic does. Returning it is what keeps
// the gateway from building a second handler for the scheduler — see the
// warning on SetupRoutes about the two PersistentStores.
func SetupServerRouter(r chi.Router, repo *db.Repo, ts *TokenSaverConfig) *chat.ChatHandler {
	// Public (unauthenticated) endpoints
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.Write([]byte(`{"status":"ok"}`))
	})
	// Version info is public (upstream PUBLIC_API_PATHS): the Sidebar polls
	// /api/version on every dashboard page including /login, before any
	// session or API key exists.
	versionH := chat.NewChatHandler(repo, ts)
	r.Get("/version", versionH.HandleVersion)
	r.Get("/api/version", versionH.HandleVersion)
	r.Get("/api/version/status", versionH.HandleVersionStatus)
	r.Get("/api/version/check", versionH.HandleCheckUpdate)
	// Embedded Native Dashboard SPA
	webH := web.Handler()
	oauthH := oauth.NewOAuthHandler(repo)
	// Public OAuth landing page: providers redirect browsers here after login.
	// Browsers carry neither the dashboard session nor the engine API key.
	r.Get("/callback", oauthH.HandleCallbackPage)
	r.Get("/", webH.ServeHTTP)
	r.Get("/login", webH.ServeHTTP)
	// Dashboard pages require the login session when requireLogin is on; the
	// guard redirects browsers to /login (upstream dashboardGuard).
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireDashboardPage(repo))
		r.Get("/dashboard", webH.ServeHTTP)
		r.Get("/dashboard/*", webH.ServeHTTP)
	})
	r.Get("/media", webH.ServeHTTP)
	r.Get("/media-providers/*", webH.ServeHTTP)
	r.Get("/media-providers/web", webH.ServeHTTP)
	r.Get("/connections", webH.ServeHTTP)
	// Provider share link: a fragment-only URL that the dashboard SPA resolves.
	// Registered here, not under /dashboard/*, because the receiver may not have
	// a session yet — the SPA gates the write itself and preserves the fragment
	// across the login redirect. The fragment never reaches the server, so a key
	// embedded in the link is never logged.
	//
	// The pattern is "/add", not "/v1/add": RequestLogger
	// (internal/middleware/logging.go:70-77) strips a leading /v1/ from
	// req.URL.Path before the router sees it, so a "/v1/add" pattern would never
	// match. /v1/add still reaches this handler — the prefix is virtual, which
	// is why every engine route is registered twice below.
	//
	// It also must not go into SetupRoutes: that whole group is mounted inside
	// RequireApiKey (router.go:627-631), so an add route registered there would
	// answer 401 to the browser that is supposed to open it.
	r.Get("/add", webH.ServeHTTP)
	r.Get("/combos", webH.ServeHTTP)
	r.Get("/analytics", webH.ServeHTTP)
	r.Get("/terminal", webH.ServeHTTP)
	r.Get("/keys", webH.ServeHTTP)
	r.Get("/settings", webH.ServeHTTP)
	r.Get("/providers", webH.ServeHTTP)
	r.Get("/usage", webH.ServeHTTP)
	r.Get("/quota", webH.ServeHTTP)
	r.HandleFunc("/assets/*", webH.ServeHTTP)
	r.HandleFunc("/providers/*", webH.ServeHTTP)
	r.HandleFunc("/icons/*", webH.ServeHTTP)
	r.HandleFunc("/favicon.ico", webH.ServeHTTP)
	r.HandleFunc("/favicon.svg", webH.ServeHTTP)
	r.HandleFunc("/icons.svg", webH.ServeHTTP)
	r.HandleFunc("/sw.js", webH.ServeHTTP)
	r.HandleFunc("/manifest.webmanifest", webH.ServeHTTP)
	r.HandleFunc("/manifest.json", webH.ServeHTTP)
	r.HandleFunc("/api/hello", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			w.Write([]byte(`{"status":"ok","message":"hello"}`))
		}
	})
	// Dashboard login session. status/login/logout/require-login are public so
	// the login page can load before a cookie exists (upstream PUBLIC_API_PATHS).
	dashH := dashboard.NewDashboardHandler(repo, ts)
	if versionH != nil {
		dashH.SemanticCache = versionH.SemanticCache
		dashH.Resilience = versionH.Resilience
	}
	// Operator-supplied dashboard assets (the white-label logo). Behind the
	// same page gate as /dashboard/*, so an install that requires a login does
	// not serve its branding to an anonymous caller.
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireDashboardPage(repo))
		r.Get("/uploads/*", dashH.HandleServeUploads)
	})
	r.Get("/api/auth/status", dashH.HandleAuthStatus)
	r.Post("/api/auth/login", dashH.HandleAuthLogin)
	r.Post("/api/auth/logout", dashH.HandleAuthLogout)
	r.Get("/api/settings/require-login", dashH.HandleRequireLogin)
	r.Get("/api/tunnel/status", dashH.HandleTunnelStatus)

	// Backend parity batch 7 — the local-only password reset (its own
	// gate lives in the handler) and the internal WorkerProxy endpoints
	// (handler-level x-internal-auth token gate; the intended caller
	// holds no dashboard session — see batch7_internal_proxy.go). The
	// session probe moved into the dashboard group in council round 3
	// (N-2 ALIGN): neither SPA consumes the unauthenticated answer, so
	// anonymous callers now get the dashboard gate's denial and session
	// callers keep the full document.
	r.Post("/api/auth/reset-password", HandleAuthResetPassword(repo))
	r.Post("/api/internal/proxy/resolve", HandleInternalProxyResolve(repo))
	r.Post("/api/internal/proxy/report", HandleInternalProxyReport(repo))

	// Admin-only operations (health reset, shutdown, update) - strictly requires session cookie or CLI token.
	// Standard client API keys are rejected, matching upstream ALWAYS_PROTECTED.
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAdminAuth())

		// Health reset endpoint — dashboard calls this via headroom proxy
		r.Post("/admin/health/reset", func(w http.ResponseWriter, r *http.Request) {
			provider := r.URL.Query().Get("provider")
			model := r.URL.Query().Get("model")
			if err := repo.ResetProviderHealth(provider, model); err != nil {
				handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
			json.MarshalWrite(w, map[string]string{"status": "ok"})
		})

		chatH := chat.NewChatHandler(repo, ts)
		r.Post("/api/version/update", chatH.HandleTriggerUpdate)
		r.Post("/api/version/auto-update", chatH.HandleToggleAutoUpdate)
		r.Post("/api/version/shutdown", HandleShutdown)

		// Profiling endpoints (pprof) — disabled by default in production for security;
		// enable explicitly via PPROF_ENABLED=true. Gated behind RequireAdminAuth
		// (issue #126) so debug surface is never exposed publicly.
		if strings.EqualFold(strings.TrimSpace(os.Getenv("PPROF_ENABLED")), "true") {
			r.HandleFunc("/debug/pprof/", pprof.Index)
			r.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
			r.HandleFunc("/debug/pprof/profile", pprof.Profile)
			r.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
			r.HandleFunc("/debug/pprof/trace", pprof.Trace)
			r.HandleFunc("/debug/pprof/*", pprof.Index)
		}
	})

	// API-key protected domain routes. The rate limiter sits after
	// RequireApiKey so it reads the per-key RPM/TPM/concurrency limits off the
	// authenticated key. Guardrails sit after both: a policy is scoped by API
	// key, and a caller the limiter already rejected must not be able to make
	// the gateway scan content either.
	// The limiter's window is a startup read: every in-memory sliding window is
	// sized against it, so changing it has to restart the gateway.
	window := time.Duration(repo.GetRateLimitDefaults().WindowSeconds) * time.Second
	rateLimiter := middleware.NewRateLimiter(window)
	// The plan tier (F-3) is resolved from the repository, so a plan change
	// applies to the next request without a restart.
	rateLimiter.SetPlanSource(repo)
	// The per-period budget (F-2) measures its spend from the same ledger the
	// Usage dashboard reads, so the ceiling and the dashboard cannot disagree
	// about what a key has spent.
	rateLimiter.SetBudgetReader(repo)
	// The fallback itself is read per request, so an operator can raise or drop
	// a global default without a restart. Only the window, which the live
	// windows already depend on, is pinned at boot.
	defaults := func() middleware.Limits {
		d := repo.GetRateLimitDefaults()
		if !d.Enabled {
			return middleware.Limits{}
		}
		return middleware.Limits{RPM: d.RPM, TPM: d.TPM, Concurrency: d.Concurrency}
	}
	guardrailStore := guardrails.NewStoreWithRebind(repo.RawDB(), repo.Rebind)
	// One switch for both taps. It reads the settings row per request, which is
	// a single indexed read on an already-loaded connection, and buys an
	// operator an off switch that does not require deleting their policies.
	guardrailSwitch := guardrails.Switch(func() bool { return repo.GetGuardrailsEnabled() })
	var engineChatH *chat.ChatHandler
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireApiKey(repo))
		r.Use(middleware.RequireRateLimit(rateLimiter, defaults, repo))
		r.Use(guardrails.Inbound(guardrailStore, guardrailAudit(repo), guardrailSwitch))
		engineChatH = SetupRoutes(r, repo, ts)

		fileH := batches.NewFileHandler(repo)
		batchH := batches.NewBatchHandler(repo)

		// File endpoints (OpenAI Batch API compatible)
		r.Post("/v1/files", fileH.HandleCreateFile)
		r.Post("/files", fileH.HandleCreateFile)
		r.Get("/v1/files", fileH.HandleListFiles)
		r.Get("/files", fileH.HandleListFiles)
		r.Get("/v1/files/{file_id}", fileH.HandleGetFile)
		r.Get("/files/{file_id}", fileH.HandleGetFile)
		r.Get("/v1/files/{file_id}/content", fileH.HandleGetFileContent)
		r.Get("/files/{file_id}/content", fileH.HandleGetFileContent)
		r.Delete("/v1/files/{file_id}", fileH.HandleDeleteFile)
		r.Delete("/files/{file_id}", fileH.HandleDeleteFile)

		// Batch endpoints (OpenAI Batch API compatible)
		r.Post("/v1/batches", batchH.HandleCreateBatch)
		r.Post("/batches", batchH.HandleCreateBatch)
		r.Get("/v1/batches", batchH.HandleListBatches)
		r.Get("/batches", batchH.HandleListBatches)
		r.Get("/v1/batches/{batch_id}", batchH.HandleGetBatch)
		r.Get("/batches/{batch_id}", batchH.HandleGetBatch)
		r.Post("/v1/batches/{batch_id}/cancel", batchH.HandleCancelBatch)
		r.Post("/batches/{batch_id}/cancel", batchH.HandleCancelBatch)
	})

	// The provider gate (docs/provider-limits-plan.md) reads the same limiter
	// for its per-node buckets, which the per-key middleware never touches.
	// Without this call the gate is inert on a real boot: its unit tests set
	// the field by hand, so only a booted router can tell the difference.
	engineChatH.SetRateLimiter(rateLimiter)

	// Dashboard management API: login-gated when requireLogin is on, but still
	// reachable with a valid API key or the local CLI token (upstream parity).
	// It reads the cache through the engine's chat handler: versionH was built
	// before any traffic, so the cache on that instance never fills.
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireDashboardAuth(repo))
		r.Use(RequireOperatorForWrites(repo, operatorWriteExempt))
		SetupDashboardRoutes(r, repo, engineChatH)
	})

	// One OpenCode sync handler serves both the dashboard-group
	// routes and the public token-authenticated ones, so its token
	// mutex covers every mutation.
	ocH := dashboard.NewOpenCodeSyncHandler(repo)

	// CLI Tools status is a dashboard read: the SPA calls it with the session
	// cookie, never an LLM API key. It was registered inside SetupRoutes, which
	// is mounted under RequireApiKey, so every dashboard call 401'd and the view
	// bounced to the endpoint tab. Reachable with a session, an API key, or the
	// local CLI token — same policy as the rest of the dashboard group.
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireDashboardAuth(repo))
		r.Use(RequireOperatorForWrites(repo, operatorWriteExempt))
		r.Get("/api/cli-tools/all-statuses", media.NewCLIToolsHandler().HandleAllStatuses)
		// CLI tool settings are dashboard reads (batch 5 re-score):
		// Axon keeps a GET-only settings route per tool next to the
		// installer, reporting install state and whether the tool's
		// own config already points at the gateway. The host-write
		// POST/DELETE sides were retired with the host-write model
		// (P7a) — the dashboard composes an installer command
		// instead of writing config files on the gateway host.
		hermesH := media.NewHermesHandler()
		r.Get("/api/cli-tools/hermes-profiles", hermesH.HandleProfiles)
		r.Get("/api/cli-tools/hermes-settings", hermesH.HandleGet)
		piH := media.NewPiHandler(repo)
		r.Get("/api/cli-tools/pi-settings", piH.HandleGet)
		settingsH := media.NewSettingsReadHandler()
		r.Get("/api/cli-tools/claude-settings", settingsH.GetClaude)
		r.Get("/api/cli-tools/codex-settings", settingsH.GetCodex)
		r.Get("/api/cli-tools/cline-settings", settingsH.GetCline)
		r.Get("/api/cli-tools/copilot-settings", settingsH.GetCopilot)
		// Cowork settings report host install state, so Axon classes
		// the route LOCAL_ONLY (dashboardGuard.js) like the OAuth
		// auto-imports — the locality wrapper is RequireLocalOnlyRoute
		// (council round 2, F-B2).
		r.Get("/api/cli-tools/cowork-settings", RequireLocalOnlyRoute(repo, settingsH.GetCowork))
		r.Get("/api/cli-tools/deepseek-tui-settings", settingsH.GetDeepseekTui)
		r.Get("/api/cli-tools/droid-settings", settingsH.GetDroid)
		r.Get("/api/cli-tools/grok-build-settings", settingsH.GetGrokBuild)
		r.Get("/api/cli-tools/jcode-settings", settingsH.GetJcode)
		r.Get("/api/cli-tools/kilo-settings", settingsH.GetKilo)
		r.Get("/api/cli-tools/openclaw-settings", settingsH.GetOpenclaw)
		r.Get("/api/cli-tools/opencode-settings", settingsH.GetOpencode)
		// Cowork MCP registry browser + tools probe.
		coworkMcpH := media.NewCoworkMcpHandler()
		r.Get("/api/cli-tools/cowork-mcp-registry", coworkMcpH.HandleRegistry)
		r.Post("/api/cli-tools/cowork-mcp-tools", coworkMcpH.HandleTools)
		// OpenCode sync management surface (Axon:
		// requireManagementAuth — local request or dashboard session).
		r.Get("/api/opencode/preferences", ocH.GetPreferences)
		r.Patch("/api/opencode/preferences", ocH.PatchPreferences)
		r.Get("/api/opencode/bundle/preview", ocH.GetBundlePreview)
		r.Get("/api/opencode/sync/tokens", ocH.GetTokens)
		r.Post("/api/opencode/sync/tokens", ocH.PostTokens)
	})

	// Public CLI-tool routes: the installer script endpoint is
	// fetched by curl/irm from the user's own terminal, where no
	// dashboard session exists (Axon lists it in the guard's public
	// paths). The OpenCode sync bundle/version authenticate with the
	// ocs_ token in-handler, and tokens/{id} carries no management
	// auth in Axon's .ts source — ported as-is.
	r.Get("/api/cli-tools/setup/{toolId}", media.NewSetupHandler().HandleSetup)
	r.Get("/api/opencode/sync/bundle", ocH.GetSyncBundle)
	r.Get("/api/opencode/sync/version", ocH.GetSyncVersion)
	r.Patch("/api/opencode/sync/tokens/{id}", ocH.PatchToken)
	r.Delete("/api/opencode/sync/tokens/{id}", ocH.DeleteToken)

	SetupConsoleLogRoutes(r, repo)
	return engineChatH
}
