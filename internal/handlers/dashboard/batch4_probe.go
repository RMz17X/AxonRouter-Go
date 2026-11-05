package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/proxy/poolstate"
)

// Shared proxy-probe engine for the batch-4 surfaces — a port of
// AxonRouter's src/lib/network/proxyTest.js (testProxyUrl /
// testProxyPoolEntry) and the health-state computation in
// src/lib/network/proxyHealth.js (computeProxyTestHealth). The health
// sweep, auto-recovery, and subscription verification all judge a proxy
// through this one door, like upstream.

const (
	proxyFailoverThreshold = 3
	proxyDeadThreshold     = 30
	defaultProbeTestURL    = "https://1.1.1.1/"
)

// proxyTestResult mirrors the JS test result: an HTTP-level answer carries
// no Error (upstream leaves it undefined there); a transport failure
// reports Status 500 with the error text.
type proxyTestResult struct {
	OK        bool
	Status    int
	ElapsedMs int64
	Error     string
}

// testProxyURL probes testURL (default https://1.1.1.1/) with a HEAD
// request through the proxy.
func testProxyURL(ctx context.Context, proxyURL, testURL string, timeout time.Duration) proxyTestResult {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return proxyTestResult{OK: false, Status: 400, Error: "proxyUrl is required"}
	}
	if strings.TrimSpace(testURL) == "" {
		testURL = defaultProbeTestURL
	}
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 8 * time.Second
	}
	// undici speaks socks5 only; a socks5h URL is rewritten before use.
	dialURL := proxyURL
	if parsed, err := url.Parse(proxyURL); err == nil && parsed.Scheme == "socks5h" {
		parsed.Scheme = "socks5"
		dialURL = parsed.String()
	}
	client, err := poolstate.ProxyClient(dialURL, timeout)
	if err != nil {
		return proxyTestResult{OK: false, Status: 400, Error: "Invalid proxy URL: " + err.Error()}
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodHead, testURL, nil)
	if err != nil {
		return proxyTestResult{OK: false, Status: 500, Error: err.Error()}
	}
	req.Header.Set("User-Agent", "AxonRouter")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		msg := err.Error()
		if probeCtx.Err() == context.DeadlineExceeded {
			msg = "Proxy test timed out"
		}
		return proxyTestResult{OK: false, Status: 500, Error: msg}
	}
	defer resp.Body.Close()
	return proxyTestResult{
		OK:        resp.StatusCode >= 200 && resp.StatusCode < 300,
		Status:    resp.StatusCode,
		ElapsedMs: time.Since(start).Milliseconds(),
	}
}

// testProxyPoolEntry probes one pool row: relay pools get the relay test
// (a plain GET with x-relay-* headers), everything else goes through
// testProxyURL.
func testProxyPoolEntry(ctx context.Context, pool map[string]any, timeout time.Duration) proxyTestResult {
	proxyURL := poolPrimaryURLValue(pool)
	if proxyURL == "" {
		return proxyTestResult{OK: false, Status: 400, Error: "Missing proxy URL"}
	}
	poolType := handlerutil.GetString(pool, "type")
	switch poolType {
	case "vercel", "cloudflare", "deno", "netlify":
		return testRelayEntry(ctx, proxyURL, timeout)
	}
	return testProxyURL(ctx, proxyURL, "", timeout)
}

func testRelayEntry(ctx context.Context, relayURL string, timeout time.Duration) proxyTestResult {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, relayURL, nil)
	if err != nil {
		return proxyTestResult{OK: false, Status: 500, Error: err.Error()}
	}
	req.Header.Set("x-relay-target", "https://httpbin.org")
	req.Header.Set("x-relay-path", "/get")
	client := &http.Client{Timeout: timeout}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		msg := err.Error()
		if probeCtx.Err() == context.DeadlineExceeded {
			msg = "Relay test timed out"
		}
		return proxyTestResult{OK: false, Status: 500, Error: msg}
	}
	defer resp.Body.Close()
	return proxyTestResult{
		OK:        resp.StatusCode >= 200 && resp.StatusCode < 300,
		Status:    resp.StatusCode,
		ElapsedMs: time.Since(start).Milliseconds(),
	}
}

// poolPrimaryURLValue reads a pool row's canonical URL (proxyUrl, else the
// first urls entry) from the dashboard's map-shaped pool rows.
func poolPrimaryURLValue(pool map[string]any) string {
	if s := handlerutil.GetString(pool, "proxyUrl"); s != "" {
		return s
	}
	if arr, ok := pool["urls"].([]any); ok {
		for _, item := range arr {
			if s, ok := item.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// computeProxyTestHealth ports upstream's health-state computation: the
// next testStatus / isActive / consecutiveFailures for a pool after one
// probe verdict (1-2 failures degraded, 3-29 unhealthy + disabled, 30+
// dead; dead is sticky until a probe passes).
func computeProxyTestHealth(pool map[string]any, res proxyTestResult) map[string]any {
	prevFailures := 0
	if v, ok := pool["consecutiveFailures"].(float64); ok {
		prevFailures = int(v)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if res.OK {
		return map[string]any{
			"testStatus":          "active",
			"isActive":            true,
			"consecutiveFailures": 0,
			"lastTestedAt":        now,
			"lastError":           nil,
		}
	}
	failures := prevFailures + 1
	prevStatus := handlerutil.GetString(pool, "testStatus")
	status := "degraded"
	switch {
	case failures >= proxyDeadThreshold || prevStatus == "dead":
		status = "dead"
	case failures >= proxyFailoverThreshold:
		status = "unhealthy"
	}
	isActive, _ := pool["isActive"].(bool)
	if failures >= proxyFailoverThreshold {
		isActive = false
	}
	lastError := res.Error
	if lastError == "" {
		if res.Status != 0 {
			lastError = fmt.Sprintf("Proxy test failed with status %d", res.Status)
		} else {
			lastError = "Proxy test failed"
		}
	}
	return map[string]any{
		"testStatus":          status,
		"isActive":            isActive,
		"consecutiveFailures": failures,
		"lastTestedAt":        now,
		"lastError":           lastError,
	}
}
