package dashboard

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// HandleVerifySubscription handles POST /api/proxy-groups/verify-subscription
// — port of AxonRouter's route.js: fetch the subscription, parse its
// proxies, and actively test a sample (up to 12) until minRequired of them
// answer.
func (h *DashboardHandler) HandleVerifySubscription(w http.ResponseWriter, r *http.Request) {
	body, err := readGroupBody(r)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	subURL := strings.TrimSpace(handlerutil.GetString(body, "url"))
	minRequired := 4
	if v, ok := body["minRequired"].(float64); ok {
		minRequired = int(v)
		if minRequired < 1 {
			minRequired = 1
		}
	}
	if subURL == "" {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Subscription URL is required"})
		return
	}
	if parsed, err := url.Parse(subURL); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid subscription URL format"})
		return
	}

	fetchCtx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, subURL, nil)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Failed to fetch subscription URL: " + err.Error()})
		return
	}
	req.Header.Set("User-Agent", "AxonRouter-ProxyAutoFetcher/1.0")
	req.Header.Set("Accept", "text/plain, application/json, */*")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Failed to fetch subscription URL: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("Subscription endpoint returned HTTP %d: %s", resp.StatusCode, http.StatusText(resp.StatusCode)),
		})
		return
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Failed to fetch subscription URL: " + err.Error()})
		return
	}

	proxies := db.ParseProxyList(string(content))
	totalCount := len(proxies)
	if totalCount == 0 {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"error":         "No valid proxies could be parsed from the subscription URL",
			"totalCount":    0,
			"verifiedCount": 0,
			"ok":            false,
		})
		return
	}

	sampleSize := totalCount
	if sampleSize > 12 {
		sampleSize = 12
	}
	candidates := proxies[:sampleSize]
	results := make([]map[string]any, len(candidates))
	var wg sync.WaitGroup
	for i, proxyURL := range candidates {
		wg.Add(1)
		go func(i int, proxyURL string) {
			defer wg.Done()
			res := testProxyURL(r.Context(), proxyURL, "", 6*time.Second)
			// Upstream drops undefined fields: a transport failure carries
			// status + error but no elapsedMs; an HTTP answer carries
			// status + elapsedMs and no error.
			entry := map[string]any{"proxyUrl": proxyURL, "ok": res.OK, "status": res.Status}
			if res.Error != "" {
				entry["error"] = res.Error
			} else {
				entry["elapsedMs"] = res.ElapsedMs
			}
			results[i] = entry
		}(i, proxyURL)
	}
	wg.Wait()

	verifiedCount := 0
	for _, entry := range results {
		if ok, _ := entry["ok"].(bool); ok {
			verifiedCount++
		}
	}
	isVerified := verifiedCount >= minRequired
	sampleResults := results
	if len(sampleResults) > 6 {
		sampleResults = sampleResults[:6]
	}
	message := fmt.Sprintf("Only %d/%d tested proxies responded (minimum %d required)", verifiedCount, len(candidates), minRequired)
	if isVerified {
		message = fmt.Sprintf("Successfully verified %d proxies (Total found: %d)", verifiedCount, totalCount)
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":            isVerified,
		"totalCount":    totalCount,
		"verifiedCount": verifiedCount,
		"minRequired":   minRequired,
		"testedCount":   len(candidates),
		"sampleResults": sampleResults,
		"message":       message,
	})
}
