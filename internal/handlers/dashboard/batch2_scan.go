package dashboard

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// POST /api/providers/{id}/scan-free-models — port of AxonRouter's
// [id]/scan-free-models route: discover candidate models for a provider,
// then probe each through a routable connection to classify it as free,
// paid, or failing. {id} here is a PROVIDER id, not a connection id.
//
// Two deviations from AxonRouter, both forced by registry data this
// codebase does not carry: AxonRouter treats a provider as dedicated-free
// when it has category "free" OR a trial key OR noAuth; the Go registry
// has no trial-key field, so the test is category "free" OR NoAuth.
// AxonRouter's SSE/JSON shapes, probe verdicts, retry counts and error
// strings are otherwise exact.

const scanMaxRetries = 3

var (
	paidScanRe  = regexp.MustCompile(`(?i)402|insufficient_user_quota|预扣费额度失败|用户剩余额度|用户额度不足|credits exhausted|insufficient credits|insufficient balance|out of credits|insufficient_quota|exceeded your current quota|PAID_MODEL_AUTH_REQUIRED|sign in to use this model|requires authentication|payment required`)
	withdrawnRe = regexp.MustCompile(`(?i)FreeTierError|can only be used from within|trial access is disabled|because of abuse`)
	timeoutRe   = regexp.MustCompile(`(?i)timeout|timed out`)
)

type scanWithdrawn struct {
	status  int
	errText string
}

// probeScanCandidate runs the up-to-3-attempts probe of one candidate.
// A non-nil withdrawn result aborts the whole scan (AxonRouter throws
// FreeTierWithdrawnError out of the probe loop).
func (h *DashboardHandler) probeScanCandidate(ctx context.Context, origin, providerID, alias string, candidate any, pool []*models.ProviderConnection) (map[string]any, *scanWithdrawn) {
	modelID := scanCandidateID(candidate)
	for _, prefix := range []string{providerID + "/", alias + "/"} {
		if strings.HasPrefix(modelID, prefix) {
			modelID = strings.TrimPrefix(modelID, prefix)
			break
		}
	}
	fullModel := alias + "/" + modelID
	name := scanCandidateName(candidate)

	connName := func(c *models.ProviderConnection) any {
		if c == nil {
			return nil
		}
		if c.Name != nil && *c.Name != "" {
			return *c.Name
		}
		if c.Email != nil && *c.Email != "" {
			return *c.Email
		}
		return nil
	}

	tried := map[string]bool{}
	var used *models.ProviderConnection
	lastStatus := 0
	lastError := ""
	attempts := 0
	for attempts < scanMaxRetries {
		attempts++
		// No-auth providers probe without any connection (AxonRouter
		// pings the bare model id); otherwise pick an untried pool member.
		var conn *models.ProviderConnection
		if len(pool) > 0 {
			available := make([]*models.ProviderConnection, 0, len(pool))
			for _, c := range pool {
				if !tried[c.ID] {
					available = append(available, c)
				}
			}
			if len(available) == 0 {
				available = pool
			}
			conn = available[rand.Intn(len(available))]
			used = conn
			tried[conn.ID] = true
		}
		connectionID := ""
		if conn != nil {
			connectionID = conn.ID
		}

		res, pingErr := h.pingModelByKind(ctx, origin, fullModel, "llm", connectionID)
		if pingErr != nil {
			message := pingErr.Error()
			if message == "" {
				message = "Probe timeout"
			}
			status := 504
			res = pingResult{OK: false, LatencyMs: 10000, Error: &message, Status: &status}
		}
		status := 0
		if res.Status != nil {
			status = *res.Status
		}
		errText := ""
		if res.Error != nil {
			errText = *res.Error
		}

		var connIDValue any
		if conn != nil {
			connIDValue = conn.ID
		}
		if res.OK {
			return map[string]any{
				"id": modelID, "name": name, "fullModel": fullModel,
				"ok": true, "isPaid": false, "latencyMs": res.LatencyMs,
				"attempts": attempts, "connectionId": connIDValue, "connectionName": connName(conn),
			}, nil
		}
		if status == 402 || paidScanRe.MatchString(errText) {
			if errText == "" {
				errText = "Paid model or insufficient credits"
			}
			return map[string]any{
				"id": modelID, "name": name, "fullModel": fullModel,
				"ok": false, "isPaid": true, "status": status, "error": errText,
				"attempts": attempts, "connectionId": connIDValue, "connectionName": connName(conn),
			}, nil
		}
		if status == 403 && withdrawnRe.MatchString(errText) {
			return nil, &scanWithdrawn{status: status, errText: errText}
		}
		lastStatus, lastError = status, errText
		if status == 504 || timeoutRe.MatchString(errText) {
			break
		}
		if attempts < scanMaxRetries {
			time.Sleep(time.Duration(350*attempts) * time.Millisecond)
		}
	}

	reason := "upstream_error"
	if lastStatus == 503 && regexp.MustCompile(`(?i)PINNED_UNAVAILABLE|not currently routable`).MatchString(lastError) {
		reason = "no_routable_account"
	} else if lastStatus == 504 {
		reason = "timeout"
	}
	if lastError == "" {
		lastError = "Probe failed after 3 attempts"
	}
	var usedID any
	if used != nil {
		usedID = used.ID
	}
	return map[string]any{
		"id": modelID, "name": name, "fullModel": fullModel,
		"ok": false, "isPaid": false, "status": lastStatus, "error": lastError,
		"reason": reason, "attempts": scanMaxRetries,
		"connectionId": usedID, "connectionName": connName(used),
	}, nil
}

func splitScanResults(results []map[string]any) (free, paid, failed []map[string]any) {
	free, paid, failed = []map[string]any{}, []map[string]any{}, []map[string]any{}
	for _, res := range results {
		switch {
		case res["ok"] == true:
			free = append(free, res)
		case res["isPaid"] == true:
			paid = append(paid, res)
		default:
			failed = append(failed, res)
		}
	}
	return free, paid, failed
}

func (h *DashboardHandler) HandleScanFreeModels(w http.ResponseWriter, r *http.Request) {
	providerID := providers.ResolveAlias(getURLParam(r, "id"))
	alias := providers.GetProviderAlias(providerID)
	meta := providers.ProviderRegistry[providerID]

	defer r.Body.Close()
	rawBody, _ := io.ReadAll(r.Body)
	var body struct {
		CandidateModels []any `json:"candidateModels"`
	}
	_ = json.Unmarshal(rawBody, &body)

	var candidates []any
	if len(body.CandidateModels) > 0 {
		candidates = body.CandidateModels
	} else {
		candidates = h.discoverScanCandidates(r.Context(), providerID, alias, meta)
	}
	if len(candidates) == 0 {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"provider": providerID, "error": "No candidate models found to probe for this provider",
			"candidates": []any{}, "freeModels": []any{}, "paidModels": []any{}, "failedModels": []any{},
		})
		return
	}

	var pool []*models.ProviderConnection
	if !meta.NoAuth {
		conns, err := h.Repo.GetProviderConnections(providerID, false)
		if err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to scan free models"})
			return
		}
		activeCount := 0
		now := time.Now()
		for _, c := range conns {
			if c.IsActive == 1 {
				activeCount++
			}
			if scanAccountEligible(c, providerID, now) {
				pool = append(pool, c)
			}
		}
		if len(pool) > 200 {
			pool = pool[:200]
		}
		if len(pool) == 0 {
			message := fmt.Sprintf("No active connections available for provider %q to run probes", providerID)
			if activeCount > 0 {
				message = fmt.Sprintf("All %d active connections for %q are locked, cooling down, or exhausted. Reset them before scanning.", activeCount, providerID)
			}
			handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
				"provider": providerID, "error": message,
				"candidates": []any{}, "freeModels": []any{}, "paidModels": []any{}, "failedModels": []any{},
			})
			return
		}
	}

	origin := handlerutil.RequestOrigin(r)
	wantsStream := strings.Contains(r.Header.Get("Accept"), "text/event-stream") ||
		strings.Contains(r.URL.RequestURI(), "stream=1")

	if wantsStream {
		h.scanFreeModelsStream(w, r, providerID, alias, candidates, pool, origin)
		return
	}

	results := []map[string]any{}
	for i := 0; i < len(candidates); i += 2 {
		end := min(i+2, len(candidates))
		chunkResults := make([]map[string]any, end-i)
		withdrawn := make([]*scanWithdrawn, end-i)
		var wg sync.WaitGroup
		for j := i; j < end; j++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				res, wd := h.probeScanCandidate(r.Context(), origin, providerID, alias, candidates[idx], pool)
				chunkResults[idx-i] = res
				withdrawn[idx-i] = wd
			}(j)
		}
		wg.Wait()
		for _, wd := range withdrawn {
			if wd != nil {
				h.writeScanWithdrawn(w, providerID, alias, *wd, results)
				return
			}
		}
		results = append(results, chunkResults...)
		if end < len(candidates) {
			time.Sleep(200 * time.Millisecond)
		}
	}

	free, paid, failed := splitScanResults(results)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider": providerID, "alias": alias,
		"totalCandidates": len(candidates), "testedCount": len(results),
		"freeModels": free, "paidModels": paid, "failedModels": failed,
		"results": results,
	})
}

func (h *DashboardHandler) writeScanWithdrawn(w http.ResponseWriter, providerID, alias string, wd scanWithdrawn, resultsSoFar []map[string]any) {
	detail := wd.errText
	if detail == "" {
		detail = fmt.Sprintf("HTTP %d", wd.status)
	}
	handlerutil.WriteJSON(w, http.StatusConflict, map[string]any{
		"provider": providerID, "alias": alias, "freeTierWithdrawn": true,
		"error":      fmt.Sprintf("Provider %q no longer offers free access: %s", providerID, detail),
		"candidates": []any{}, "freeModels": []any{}, "paidModels": []any{}, "failedModels": []any{},
		"results": resultsSoFar,
	})
}

func (h *DashboardHandler) scanFreeModelsStream(w http.ResponseWriter, r *http.Request, providerID, alias string, candidates []any, pool []*models.ProviderConnection, origin string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to scan free models"})
		return
	}
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	var mu sync.Mutex
	send := func(event string, payload map[string]any) {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, encoded)
		flusher.Flush()
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				mu.Lock()
				fmt.Fprint(w, ": keep-alive\n\n")
				flusher.Flush()
				mu.Unlock()
			}
		}
	}()

	send("start", map[string]any{
		"provider": providerID, "alias": alias, "totalCandidates": len(candidates),
	})

	results := []map[string]any{}
	for i := 0; i < len(candidates); i += 4 {
		end := min(i+4, len(candidates))
		chunkResults := make([]map[string]any, end-i)
		withdrawn := make([]*scanWithdrawn, end-i)
		var wg sync.WaitGroup
		for j := i; j < end; j++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				res, wd := h.probeScanCandidate(r.Context(), origin, providerID, alias, candidates[idx], pool)
				chunkResults[idx-i] = res
				withdrawn[idx-i] = wd
			}(j)
		}
		wg.Wait()
		aborted := false
		for _, wd := range withdrawn {
			if wd != nil {
				detail := wd.errText
				if detail == "" {
					detail = fmt.Sprintf("HTTP %d", wd.status)
				}
				send("freeTierWithdrawn", map[string]any{
					"provider": providerID,
					"error":    fmt.Sprintf("Provider %q no longer offers free access: %s", providerID, detail),
				})
				aborted = true
				break
			}
		}
		if aborted {
			return
		}
		for _, res := range chunkResults {
			results = append(results, res)
			event := make(map[string]any, len(res)+2)
			for k, v := range res {
				event[k] = v
			}
			event["testedCount"] = len(results)
			event["totalCandidates"] = len(candidates)
			send("probe", event)
		}
		if end < len(candidates) {
			time.Sleep(50 * time.Millisecond)
		}
	}

	free, paid, failed := splitScanResults(results)
	send("done", map[string]any{
		"provider": providerID, "alias": alias,
		"totalCandidates": len(candidates), "testedCount": len(results),
		"freeModels": free, "paidModels": paid, "failedModels": failed,
		"results": results,
	})
}
