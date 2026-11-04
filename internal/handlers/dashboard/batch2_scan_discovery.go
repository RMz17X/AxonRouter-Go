package dashboard

import (
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

func scanCandidateID(item any) string {
	switch t := item.(type) {
	case string:
		return t
	case map[string]any:
		return dataStr(t, "id")
	}
	return ""
}

func scanCandidateName(item any) string {
	switch t := item.(type) {
	case string:
		return t
	case map[string]any:
		if name := dataStr(t, "name"); name != "" {
			return name
		}
		return dataStr(t, "id")
	}
	return ""
}

func scanLooksFree(item any) bool {
	m := asModelMap(item)
	if m != nil && m["isFree"] == true {
		return true
	}
	if m != nil {
		if credit, ok := m["credit"].(float64); ok && credit == 0 {
			return true
		}
	}
	id := scanCandidateID(item)
	name := strings.ToLower(scanCandidateName(item))
	return strings.HasSuffix(id, ":free") || strings.HasSuffix(id, "-free") ||
		strings.Contains(id, "/free") || strings.Contains(id, ":free/") ||
		strings.Contains(name, "(free)") || strings.Contains(name, " free") ||
		providers.IsFreeTierModel(id)
}

// fetchScanJSON GETs a JSON document with the given timeout; any failure
// yields nil (AxonRouter swallows each discovery source's errors).
func fetchScanJSON(ctx context.Context, url string, timeout time.Duration) any {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil
	}
	var payload any
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil || json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	return payload
}

func extractScanList(payload any) []any {
	if list := asAnyList(payload); list != nil {
		return list
	}
	if m := asModelMap(payload); m != nil {
		if list := asAnyList(m["data"]); list != nil {
			return list
		}
		if list := asAnyList(m["models"]); list != nil {
			return list
		}
	}
	return nil
}

// discoverScanCandidates mirrors getScanCandidates: registry models first
// (all of them for dedicated-free providers, free-shaped ones otherwise),
// then the provider's dynamic sources.
func (h *DashboardHandler) discoverScanCandidates(ctx context.Context, providerID, alias string, meta providers.ProviderMeta) []any {
	dedicatedFree := meta.Category == providers.CategoryFree || meta.NoAuth
	seen := map[string]bool{}
	candidates := []any{}
	add := func(item any) {
		key := strings.TrimSpace(scanCandidateID(item))
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		candidates = append(candidates, item)
	}

	registryIDs := []string{}
	registryIDs = append(registryIDs, providers.GetProviderModels(providerID)...)
	if alias != providerID {
		registryIDs = append(registryIDs, providers.GetProviderModels(alias)...)
	}
	for _, id := range registryIDs {
		item := map[string]any{"id": id, "name": id}
		if dedicatedFree || scanLooksFree(item) {
			add(item)
		}
	}

	switch providerID {
	case "cline-free":
		if payload := fetchScanJSON(ctx, "https://api.cline.bot/api/v1/ai/cline/recommended-models", 8*time.Second); payload != nil {
			var list []any
			if m := asModelMap(payload); m != nil {
				list = asAnyList(m["free"])
			}
			for _, item := range list {
				add(item)
			}
		}
		if payload := fetchScanJSON(ctx, "https://api.cline.bot/api/v1/models", 8*time.Second); payload != nil {
			for _, item := range extractScanList(payload) {
				add(item)
			}
		}
	case "kilocode", "kilocode-free":
		if payload := fetchScanJSON(ctx, kiloModelsURL, 10*time.Second); payload != nil {
			for _, item := range extractScanList(payload) {
				m := asModelMap(item)
				if m == nil {
					continue
				}
				id := dataStr(m, "id")
				if m["isFree"] == true || strings.HasSuffix(id, ":free") || strings.HasSuffix(id, "-free") || id == "kilo-auto/free" {
					add(item)
				}
			}
		}
	case "opencode", "opencode-zen":
		if payload := fetchScanJSON(ctx, "https://opencode.ai/zen/v1/models", 8*time.Second); payload != nil {
			if filtered, ok := applyModelFetcherFilter("opencode-free", extractScanList(payload)); ok {
				for _, item := range filtered {
					add(item)
				}
			}
		}
	default:
		if meta.ModelsFetcher != nil && meta.ModelsFetcher.URL != "" {
			if payload := fetchScanJSON(ctx, meta.ModelsFetcher.URL, 10*time.Second); payload != nil {
				raw := extractScanList(payload)
				if raw == nil {
					raw = asAnyList(payload)
				}
				var items []any
				if filtered, ok := applyModelFetcherFilter(meta.ModelsFetcher.Type, raw); ok {
					items = filtered
				} else if raw != nil {
					items = raw
				}
				for _, item := range items {
					add(item)
				}
			}
		}
	}
	return candidates
}

// scanAccountEligible mirrors the route's routable-connection predicate.
func scanAccountEligible(c *models.ProviderConnection, providerID string, now time.Time) bool {
	if c.IsActive != 1 {
		return false
	}
	data := connDataMap(c)
	testStatus := dataStr(data, "testStatus")
	if testStatus == "" {
		testStatus = "active"
	}
	if badTestStatuses[testStatus] {
		return false
	}
	if refreshBlocked(dataMap(data, "providerSpecificData")) {
		return false
	}
	if fatalConnectionError.MatchString(lastErrorText(data)) {
		return false
	}
	locks := activeModelLocks(data, now)
	if _, locked := locks["__all"]; locked {
		return false
	}
	if _, ok := futureTime(data, "lockedAllUntil", now); ok {
		return false
	}
	if _, ok := futureTime(data, "rateLimitedUntil", now); ok {
		return false
	}
	if testStatus == "exhausted" {
		if providerID != "antigravity" {
			return false
		}
		if !antigravityBothFamiliesExhausted(dataMap(data, "quotas")) {
			return false
		}
	}
	return true
}
