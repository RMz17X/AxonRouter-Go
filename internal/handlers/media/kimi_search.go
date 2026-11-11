package media

import (
	"errors"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"time"

	"axonrouter/proxy/internal/handlers/chat"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/models"
	"axonrouter/proxy/internal/proxy"
	"axonrouter/proxy/internal/proxy/executor"
)

// handleKimiWebSearch serves /v1/search for provider kimi-web: the
// search runs as a use_search completion on a fleet connection (see
// executor.ExecuteKimiWebSearch) and the stream's structured
// search_plus results become the standard SearchResponse envelope,
// the same shape the antigravity adapter returns. Kimi's search
// lives on this media surface, not in the chat model catalog.
func (h *MediaHandler) handleKimiWebSearch(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
	var reqBody struct {
		Query      string `json:"query"`
		Prompt     string `json:"prompt"`
		MaxResults int    `json:"max_results"`
	}
	_ = json.Unmarshal(body, &reqBody)
	query := sanitizeSearchQuery(reqBody.Query)
	if query == "" {
		query = sanitizeSearchQuery(reqBody.Prompt)
	}
	if query == "" {
		return searchError(http.StatusBadRequest, "missing query in search request")
	}
	limit := reqBody.MaxResults
	if limit <= 0 {
		limit = defaultSearchMaxResults
	}
	if limit > maxSearchMaxResults {
		limit = maxSearchMaxResults
	}

	excludeIDs := []string{}
	usePinned := modelInfo.ConnectionID != ""
	var lastErr *searchUpstreamError
	for {
		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), "kimi-web", modelInfo.ConnectionID, excludeIDs, modelInfo.Model)
		if err != nil || conn == nil {
			if lastErr != nil {
				return searchError(lastErr.Status, fmt.Sprintf("all kimi-web accounts failed, last error: %s", lastErr.Message))
			}
			return searchError(http.StatusBadRequest, fmt.Sprintf("no active connection for kimi-web: %v", err))
		}

		attemptErr := h.tryKimiWebSearchConn(w, r, query, limit, conn, connData)
		if attemptErr == nil {
			return nil
		}
		lastErr = attemptErr
		if usePinned || !attemptErr.Retryable {
			return lastErr
		}
		excludeIDs = append(excludeIDs, conn.ID)
	}
}

func (h *MediaHandler) tryKimiWebSearchConn(w http.ResponseWriter, r *http.Request, query string, maxResults int, conn *models.ProviderConnection, connData *chat.ConnectionData) *searchUpstreamError {
	startTime := time.Now()

	client, err := h.ChatH.GetClientForConnection(connData)
	if err != nil {
		return &searchUpstreamError{Message: err.Error()}
	}
	apiKey := chat.ExtractAPIKey(connData)
	execReq := &executor.Request{
		Ctx:          r.Context(),
		Client:       client,
		Provider:     "kimi-web",
		APIKey:       apiKey,
		ConnectionID: conn.ID,
		ConnData:     connData.ProviderSpecificData,
		ModelName:    "kimi-search",
	}
	outcome, err := executor.ExecuteKimiWebSearch(r.Context(), execReq, query, maxResults)
	if err != nil {
		status := http.StatusBadGateway
		var uerr *proxy.UpstreamError
		if errors.As(err, &uerr) && uerr.StatusCode > 0 {
			status = uerr.StatusCode
		}
		// 400/422 are the request's fault (validation), never the
		// account's: do not rotate on them. Auth, rate-limit and
		// upstream failures may succeed on the next account.
		retryable := status != http.StatusBadRequest && status != http.StatusUnprocessableEntity
		return &searchUpstreamError{Status: status, Message: err.Error(), Retryable: retryable}
	}

	if h.Repo != nil {
		h.Repo.UpdateConnectionLastUsed(conn.ID)
	}
	retrievedAt := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	results := make([]SearchResult, 0, len(outcome.Results))
	for idx, res := range outcome.Results {
		var publishedAt any
		if res.PublishedAt != "" {
			publishedAt = res.PublishedAt
		}
		var favicon any
		if res.FaviconURL != "" {
			favicon = res.FaviconURL
		}
		results = append(results, SearchResult{
			Title:       res.Title,
			URL:         res.URL,
			Snippet:     res.Snippet,
			Position:    idx + 1,
			Score:       nil,
			PublishedAt: publishedAt,
			FaviconURL:  favicon,
			Content:     res.Snippet,
			Metadata:    map[string]any{},
			Citation: SearchCitation{
				Provider:    "kimi-web",
				RetrievedAt: retrievedAt,
				Rank:        idx + 1,
			},
			ProviderRaw: nil,
		})
	}

	responseTimeMs := time.Since(startTime).Milliseconds()
	log.Info("request", "POST /v1/search", "provider", "kimi-web", "model", "kimi-search", "query", query, "results", len(results), "conn", conn.ID[:min(8, len(conn.ID))])
	searchResponse := SearchResponse{
		Provider: "kimi-web",
		Query:    query,
		Results:  results,
		Answer: &SearchAnswer{
			Source: "kimi-web",
			Text:   outcome.Answer,
			Model:  "kimi-search",
		},
		Usage: SearchUsage{
			QueriesUsed:   1,
			SearchCostUSD: 0,
			LLMTokens:     0,
		},
		Metrics: SearchMetrics{
			ResponseTimeMS:        responseTimeMs,
			UpstreamLatencyMS:     responseTimeMs,
			TotalResultsAvailable: nil,
		},
		Errors: []any{},
	}
	handlerutil.WriteJSON(w, http.StatusOK, searchResponse)
	return nil
}
