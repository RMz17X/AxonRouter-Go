package dashboard

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/models"
)

var (
	miniMaxClaimMu      sync.Mutex
	miniMaxClaimRunning bool
)

// MiniMaxClaimResult holds the outcome of a claim attempt for one account.
type MiniMaxClaimResult struct {
	ConnectionID string `json:"connectionId"`
	Name         string `json:"name"`
	Provider     string `json:"provider"`
	Status       string `json:"status"` // "success", "already_claimed", "error", "skipped"
	Claimed      bool   `json:"claimed"`
	Points       int    `json:"points"`
	StreakDay    int    `json:"streakDay"`
	Message      string `json:"message"`
	Error        string `json:"error,omitempty"`
}

// MiniMaxClaimSummary aggregates batch claim results.
type MiniMaxClaimSummary struct {
	Total          int `json:"total"`
	Claimed        int `json:"claimed"`
	AlreadyClaimed int `json:"alreadyClaimed"`
	Errors         int `json:"errors"`
	Skipped        int `json:"skipped"`
	TotalPoints    int `json:"totalPoints"`
}

// MiniMaxClaimAllResponse represents the JSON response for bulk claim operations.
type MiniMaxClaimAllResponse struct {
	Success bool                 `json:"success"`
	Summary MiniMaxClaimSummary  `json:"summary"`
	Results []MiniMaxClaimResult `json:"results"`
}

// MiniMaxSignInStatus represents current check-in streak and today's status.
type MiniMaxSignInStatus struct {
	ConnectionID string `json:"connectionId"`
	TodayStatus  int    `json:"todayStatus"` // 2 = ready to claim, 3 = already claimed today, 1 = upcoming
	DayNo        int    `json:"dayNo"`
	Points       int    `json:"points"`
	IsClaimed    bool   `json:"isClaimed"`
	Days         []any  `json:"days,omitempty"`
}

// CheckMiniMaxSignInStatus queries the check-in status for an account.
func CheckMiniMaxSignInStatus(ctx context.Context, site miniMaxCodeSite, access, userID string) (int, int, int, []any, error) {
	env, err := miniMaxCodeAccountCall(ctx, site, "/minimax-cloud/api/v1/signin/status", access, userID, nil, "minimax signin status")
	if err != nil {
		return 0, 0, 0, nil, err
	}
	data := nestedMap(env, "data")
	if data == nil {
		return 0, 0, 0, nil, errors.New("no data in signin status reply")
	}
	days, _ := data["days"].([]any)
	for _, raw := range days {
		day, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if isToday, _ := day["is_today"].(bool); isToday {
			status := int(usageNum(day["status"], 0))
			dNo := int(usageNum(day["day_no"], 0))
			pts := int(usageNum(day["points"], 0))
			return status, dNo, pts, days, nil
		}
	}
	return 0, 0, 0, days, errors.New("today status not found in signin days")
}

// ClaimMiniMaxReward claims the daily checkin points on MiniMax Cloud.
func ClaimMiniMaxReward(ctx context.Context, site miniMaxCodeSite, access, userID string) (int, int, error) {
	env, err := miniMaxCodeAccountCall(ctx, site, "/minimax-cloud/api/v1/signin/claim", access, userID, map[string]any{}, "minimax signin claim")
	if err != nil {
		return 0, 0, err
	}
	data := nestedMap(env, "data")
	if data == nil {
		return 0, 0, errors.New("no data in signin claim reply")
	}
	points := int(usageNum(data["points"], 0))
	dayNo := int(usageNum(data["day_no"], 0))
	return points, dayNo, nil
}

// resolveMiniMaxSite maps the connection's provider to the corresponding mcode site.
func resolveMiniMaxSite(provider string) (miniMaxCodeSite, bool) {
	if site, ok := miniMaxCodeSites[provider]; ok {
		return site, true
	}
	if provider == "minimax" {
		return miniMaxCodeSites["minimax-code-global"], true
	}
	return miniMaxCodeSite{}, false
}

// ClaimMiniMaxForConnection executes status verification and claiming for a single connection.
func ClaimMiniMaxForConnection(ctx context.Context, repo *db.Repo, conn *models.ProviderConnection) MiniMaxClaimResult {
	connName := conn.ID
	if conn.Name != nil && *conn.Name != "" {
		connName = *conn.Name
	}

	res := MiniMaxClaimResult{
		ConnectionID: conn.ID,
		Name:         connName,
		Provider:     conn.Provider,
		Status:       "error",
	}

	site, ok := resolveMiniMaxSite(conn.Provider)
	if !ok {
		res.Status = "skipped"
		res.Message = fmt.Sprintf("Provider %s is not a MiniMax Code provider", conn.Provider)
		return res
	}

	var data map[string]any
	if conn.Data != "" {
		_ = json.Unmarshal([]byte(conn.Data), &data)
	}
	accessToken, _, psd := usageCreds(data)
	if accessToken == "" {
		res.Message = "No access token found"
		res.Error = res.Message
		return res
	}

	realUserID := firstNonEmptyStr(usageStr(psd["realUserID"]), usageStr(psd["real_user_id"]))
	if realUserID == "" {
		who, err := miniMaxCodeIdentityOf(ctx, site, accessToken)
		if err == nil && who.realUserID != "" {
			realUserID = who.realUserID
		}
	}

	// 1. Check current check-in status
	todayStatus, dayNo, pts, _, err := CheckMiniMaxSignInStatus(ctx, site, accessToken, realUserID)
	if err != nil {
		res.Message = fmt.Sprintf("Failed to check status: %v", err)
		res.Error = err.Error()
		log.Printf("[minimax-claim] Account %s (%s): %s", connName, conn.ID, res.Message)
		return res
	}

	res.StreakDay = dayNo

	// Status 3 means already checked in today
	if todayStatus == 3 {
		res.Status = "already_claimed"
		res.Claimed = false
		res.Points = pts
		res.Message = fmt.Sprintf("Already claimed for today (Streak Day %d)", dayNo)
		log.Printf("[minimax-claim] Account %s (%s): Already claimed today (Day %d)", connName, conn.ID, dayNo)
		return res
	}

	// 2. Claim points
	claimedPts, claimedDay, claimErr := ClaimMiniMaxReward(ctx, site, accessToken, realUserID)
	if claimErr != nil {
		res.Status = "error"
		res.Message = fmt.Sprintf("Claim failed: %v", claimErr)
		res.Error = claimErr.Error()
		log.Printf("[minimax-claim] Account %s (%s): Claim failed: %v", connName, conn.ID, claimErr)
		return res
	}

	if claimedPts == 0 && pts > 0 {
		claimedPts = pts
	}
	if claimedDay == 0 && dayNo > 0 {
		claimedDay = dayNo
	}

	res.Status = "success"
	res.Claimed = true
	res.Points = claimedPts
	res.StreakDay = claimedDay
	res.Message = fmt.Sprintf("Successfully claimed %d points for Day %d!", claimedPts, claimedDay)

	// Invalidate quota usage cache so the tracker shows fresh points immediately
	InvalidateUsageCache(conn.ID)

	log.Printf("[minimax-claim] Account %s (%s): Claimed %d points (Day %d)", connName, conn.ID, claimedPts, claimedDay)
	return res
}

// ClaimAllMiniMaxConnections loops over all active MiniMax connections and claims rewards.
func ClaimAllMiniMaxConnections(ctx context.Context, repo *db.Repo) (MiniMaxClaimSummary, []MiniMaxClaimResult, error) {
	if repo == nil {
		return MiniMaxClaimSummary{}, nil, errors.New("repository not available")
	}

	miniMaxClaimMu.Lock()
	if miniMaxClaimRunning {
		miniMaxClaimMu.Unlock()
		return MiniMaxClaimSummary{}, nil, errors.New("claim process is already running")
	}
	miniMaxClaimRunning = true
	miniMaxClaimMu.Unlock()

	defer func() {
		miniMaxClaimMu.Lock()
		miniMaxClaimRunning = false
		miniMaxClaimMu.Unlock()
	}()

	var allConns []*models.ProviderConnection
	seen := make(map[string]bool)

	for _, prov := range []string{"minimax-code-global", "minimax-code", "minimax"} {
		conns, err := repo.GetProviderConnections(prov, true)
		if err != nil {
			continue
		}
		for _, c := range conns {
			if c != nil && !seen[c.ID] {
				seen[c.ID] = true
				allConns = append(allConns, c)
			}
		}
	}

	summary := MiniMaxClaimSummary{
		Total: len(allConns),
	}
	results := make([]MiniMaxClaimResult, 0, len(allConns))

	log.Printf("[minimax-claim] Starting check across %d active MiniMax connection(s)...", len(allConns))

	for i, conn := range allConns {
		select {
		case <-ctx.Done():
			log.Printf("[minimax-claim] Aborted by context after %d accounts", i)
			return summary, results, ctx.Err()
		default:
		}

		connName := conn.ID
		if conn.Name != nil && *conn.Name != "" {
			connName = *conn.Name
		}
		log.Printf("[minimax-claim] [%d/%d] Processing %s (%s)...", i+1, len(allConns), connName, conn.ID)

		res := ClaimMiniMaxForConnection(ctx, repo, conn)
		results = append(results, res)

		switch res.Status {
		case "success":
			summary.Claimed++
			summary.TotalPoints += res.Points
		case "already_claimed":
			summary.AlreadyClaimed++
		case "skipped":
			summary.Skipped++
		default:
			summary.Errors++
		}

		// Gentle throttling between requests to avoid rate limits
		if i+1 < len(allConns) {
			select {
			case <-ctx.Done():
				return summary, results, ctx.Err()
			case <-time.After(350 * time.Millisecond):
			}
		}
	}

	log.Printf("[minimax-claim] Summary: %d total, %d newly claimed (%d pts), %d already claimed, %d errors",
		summary.Total, summary.Claimed, summary.TotalPoints, summary.AlreadyClaimed, summary.Errors)

	return summary, results, nil
}

// HandleClaimMiniMaxReward handles POST /api/usage/{connectionId}/minimax-claim.
func (h *DashboardHandler) HandleClaimMiniMaxReward(w http.ResponseWriter, r *http.Request) {
	connID := getURLParam(r, "connectionId")
	if connID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing connectionId")
		return
	}

	conn, err := h.Repo.GetProviderConnectionByID(connID)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conn == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "connection not found")
		return
	}

	res := ClaimMiniMaxForConnection(r.Context(), h.Repo, conn)
	if res.Claimed || res.Status == "already_claimed" {
		invalidateClaimStatus(connID)
	}
	handlerutil.WriteJSON(w, http.StatusOK, res)
}

// HandleGetMiniMaxClaimStatus handles GET /api/usage/{connectionId}/minimax-claim.
func (h *DashboardHandler) HandleGetMiniMaxClaimStatus(w http.ResponseWriter, r *http.Request) {
	connID := getURLParam(r, "connectionId")
	if connID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing connectionId")
		return
	}

	if cached, ok := cachedClaimStatus(connID); ok {
		handlerutil.WriteJSON(w, http.StatusOK, cached)
		return
	}

	conn, err := h.Repo.GetProviderConnectionByID(connID)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conn == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "connection not found")
		return
	}

	site, ok := resolveMiniMaxSite(conn.Provider)
	if !ok {
		writePlainError(w, http.StatusBadRequest, "Provider is not MiniMax")
		return
	}

	var data map[string]any
	if conn.Data != "" {
		_ = json.Unmarshal([]byte(conn.Data), &data)
	}
	accessToken, _, psd := usageCreds(data)
	if accessToken == "" {
		writePlainError(w, http.StatusBadRequest, "No access token found")
		return
	}
	realUserID := firstNonEmptyStr(usageStr(psd["realUserID"]), usageStr(psd["real_user_id"]))

	todayStatus, dayNo, pts, days, err := CheckMiniMaxSignInStatus(r.Context(), site, accessToken, realUserID)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}

	resp := MiniMaxSignInStatus{
		ConnectionID: connID,
		TodayStatus:  todayStatus,
		DayNo:        dayNo,
		Points:       pts,
		IsClaimed:    todayStatus == 3,
		Days:         days,
	}
	storeClaimStatus(connID, resp)

	handlerutil.WriteJSON(w, http.StatusOK, resp)
}

// HandleClaimAllMiniMaxRewards handles POST /api/providers/minimax/claim-all.
func (h *DashboardHandler) HandleClaimAllMiniMaxRewards(w http.ResponseWriter, r *http.Request) {
	summary, results, err := ClaimAllMiniMaxConnections(r.Context(), h.Repo)
	if err != nil {
		if strings.Contains(err.Error(), "already running") {
			handlerutil.WriteJSONError(w, http.StatusConflict, err.Error())
			return
		}
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	invalidateClaimStatus("")

	handlerutil.WriteJSON(w, http.StatusOK, MiniMaxClaimAllResponse{
		Success: true,
		Summary: summary,
		Results: results,
	})
}
