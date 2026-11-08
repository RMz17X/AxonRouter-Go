package handlers

import (
	"net/http"
	"strconv"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/translator"
	"axonrouter/proxy/internal/usagetracker"
)

// axonActiveRequest is AxonRouter's activeRequests item (its
// getActiveRequests): exactly the four fields, without the requested/combo
// names /api/usage/stats adds for the topology.
type axonActiveRequest struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Account  string `json:"account"`
	Count    int    `json:"count"`
	IsStream bool   `json:"isStream"`
}

func axonActiveRequests(items []usagetracker.ActiveRequest) []axonActiveRequest {
	out := make([]axonActiveRequest, 0, len(items))
	for _, item := range items {
		out = append(out, axonActiveRequest{
			Model:    item.Model,
			Provider: item.Provider,
			Account:  item.Account,
			Count:    item.Count,
			IsStream: item.IsStream,
		})
	}
	return out
}

// axonRecentRequest is AxonRouter's recentRequests item on its lightweight
// reads (getRecentUsageRowsFromDb): the history row's own fields only — no
// cached tokens, account or key, which the full stats payload adds.
type axonRecentRequest struct {
	Timestamp        string `json:"timestamp"`
	Model            string `json:"model"`
	Provider         string `json:"provider"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	// Tokens is the row total (prompt + completion). The analytics
	// recent-requests merge compares it to spot the newest request;
	// without it that pulse compared two absent fields and never fired.
	Tokens           int    `json:"tokens"`
	Status           string `json:"status"`
	Endpoint         string `json:"endpoint"`
}

// axonRecentRequests folds the newest history rows the way AxonRouter's
// getRecentUsageRowsFromDb does: only rows that moved tokens survive,
// deduped on model|provider|tokens|minute, newest first. Token counts are
// resolved through the translator like every other history read in this
// package (the stored payload can carry the counts when the columns are 0),
// and the status is normalized to this build's wire vocabulary the way
// /api/usage/stats normalizes it ("success" reads as "ok").
func axonRecentRequests(repo *db.Repo, limit int) ([]axonRecentRequest, error) {
	rows, err := repo.GetRecentUsageHistory(60)
	if err != nil {
		return nil, err
	}
	out := make([]axonRecentRequest, 0, limit)
	seen := make(map[string]bool)
	for _, rh := range rows {
		promptTok, complTok := translator.ResolveTokensCounts([]byte(rh.Tokens), rh.PromptTokens, rh.CompletionTokens)
		if promptTok == 0 && complTok == 0 {
			continue
		}
		minute := ""
		if len(rh.Timestamp) >= 16 {
			minute = rh.Timestamp[:16]
		}
		key := rh.Model + "|" + rh.Provider + "|" + strconv.FormatInt(promptTok, 10) + "|" + strconv.FormatInt(complTok, 10) + "|" + minute
		if seen[key] {
			continue
		}
		seen[key] = true

		model := rh.Model
		if model == "" {
			model = "unknown"
		}
		provider := rh.Provider
		if provider == "" {
			provider = "unknown"
		}
		status := "ok"
		if rh.Status != "" && rh.Status != "success" && rh.Status != "ok" {
			status = rh.Status
		}
		endpoint := rh.Endpoint
		if endpoint == "" {
			endpoint = "Unknown"
		}
		out = append(out, axonRecentRequest{
			Timestamp:        rh.Timestamp,
			Model:            model,
			Provider:         provider,
			PromptTokens:     int(promptTok),
			CompletionTokens: int(complTok),
			Tokens:           int(promptTok + complTok),
			Status:           status,
			Endpoint:         endpoint,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// HandleUsageActiveRequests serves GET /api/usage/active-requests —
// AxonRouter's lightweight hydration read: the in-flight requests and
// pending counts from the tracker, plus the newest history rows, without the
// full stats aggregation. Its error body is AxonRouter's own shape (empty
// lists plus the message), not the standard error envelope.
func HandleUsageActiveRequests(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state := usagetracker.GetTracker().GetActiveState(repo)
		recent, err := axonRecentRequests(repo, 20)
		if err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
				"activeRequests": []any{},
				"recentRequests": []any{},
				"error":          err.Error(),
			})
			return
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"activeRequests": axonActiveRequests(state.ActiveRequests),
			"recentRequests": recent,
			"errorProvider":  state.ErrorProvider,
			"pending":        state.Pending,
		})
	}
}
