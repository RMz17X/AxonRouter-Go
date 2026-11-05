package handlers

import (
	json "encoding/json/v2"
	"net/http"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
)

// usageLogItem is AxonRouter's log row (its mapLogRow over
// usage_request_logs). This build never had that table — usageHistory is
// the same one-row-per-request record — so the projection differs where the
// source does: there is no stored request id (it is lifted from the row's
// meta payload when the writer recorded one there) and every row is the
// gateway's own, which is what AxonRouter's source "general" selects.
type usageLogItem struct {
	ID               int64   `json:"id"`
	Timestamp        string  `json:"timestamp"`
	RequestID        *string `json:"requestId"`
	Provider         *string `json:"provider"`
	Model            *string `json:"model"`
	ConnectionID     *string `json:"connectionId"`
	Status           string  `json:"status"`
	PromptTokens     *int64  `json:"promptTokens"`
	CompletionTokens *int64  `json:"completionTokens"`
	Source           string  `json:"source"`
	Metadata         any     `json:"metadata"`
}

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// HandleUsageLogs serves GET /api/usage/logs: the newest 200 usage rows in
// log shape, as a bare array.
func HandleUsageLogs(repo *db.Repo) http.HandlerFunc {
	return handleUsageLogRows(repo)
}

// HandleUsageRequestLogs serves GET /api/usage/request-logs. AxonRouter's
// two routes are the same read under two names; so are these.
func HandleUsageRequestLogs(repo *db.Repo) http.HandlerFunc {
	return handleUsageLogRows(repo)
}

func handleUsageLogRows(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := repo.GetRecentUsageLogRows(r.Context(), 200)
		if err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to fetch logs"})
			return
		}
		out := make([]usageLogItem, 0, len(rows))
		for _, row := range rows {
			item := usageLogItem{
				ID:           row.ID,
				Timestamp:    row.Timestamp,
				Provider:     strOrNil(row.Provider),
				Model:        strOrNil(row.Model),
				ConnectionID: strOrNil(row.ConnectionID),
				Status:       row.Status,
				Source:       "general",
			}
			if row.PromptTokens.Valid {
				item.PromptTokens = &row.PromptTokens.Int64
			}
			if row.CompletionTokens.Valid {
				item.CompletionTokens = &row.CompletionTokens.Int64
			}
			if row.Meta != "" {
				var meta any
				if err := json.Unmarshal([]byte(row.Meta), &meta); err == nil {
					item.Metadata = meta
					if m, ok := meta.(map[string]any); ok {
						for _, key := range []string{"requestId", "request_id"} {
							if v, ok := m[key].(string); ok && v != "" {
								item.RequestID = &v
								break
							}
						}
					}
				}
			}
			out = append(out, item)
		}
		handlerutil.WriteJSON(w, http.StatusOK, out)
	}
}
