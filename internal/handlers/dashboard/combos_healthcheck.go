package dashboard

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
	"github.com/google/uuid"
)

// Combo test — port of AxonRouter's POST /api/combos/test
// (src/app/api/combos/test/route.ts). Every leaf model of the combo (nested
// combos expanded) gets one health-check chat request through this same
// gateway, and the per-target outcomes come back in one response.
//
// Two Axon details could not be carried over literally, both because the
// machinery they steer does not exist here: combos have no config object, so
// the max depth is always Axon's default 8 rather than a per-combo
// maxComboDepth; and member steps carry no connectionId, so the
// X-OmniRoute-Connection pinning header is never sent (targets always have a
// null connectionId, like Axon steps without one).

const (
	comboTestMaxDepth    = 8
	comboTestConcurrency = 4
	comboTestTimeout     = 20 * time.Second
)

// comboTestTarget is one resolved leaf of the combo: the model string the
// health check will call, and the step metadata Axon reports for it. The
// pointer fields are always null here — 9router-go combo members are plain
// model strings with no step id, connection pin, or label.
type comboTestTarget struct {
	ModelStr     string  `json:"modelStr"`
	Provider     string  `json:"provider"`
	StepID       *string `json:"stepId"`
	ExecutionKey string  `json:"executionKey"`
	ConnectionID *string `json:"connectionId"`
	Label        *string `json:"label"`
}

// comboTestResult is one target's outcome. StatusCode appears only when an
// HTTP response was received, ResponseText only on success, Error only on
// failure — Axon's two result variants.
type comboTestResult struct {
	comboTestTarget
	Status       string `json:"status"`
	LatencyMs    int64  `json:"latencyMs"`
	StatusCode   *int   `json:"statusCode,omitempty"`
	Error        string `json:"error,omitempty"`
	ResponseText string `json:"responseText,omitempty"`
}

// HandleTestCombo handles POST /api/combos/test with body { comboName }.
func (h *DashboardHandler) HandleTestCombo(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeComboTestValidation(w, "Invalid request", "body", "Invalid JSON body")
		return
	}
	defer r.Body.Close()

	var body struct {
		ComboName string `json:"comboName"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		writeComboTestValidation(w, "Invalid request", "body", "Invalid JSON body")
		return
	}
	comboName := strings.TrimSpace(body.ComboName)
	if comboName == "" {
		writeComboTestValidation(w, "Validation failed", "comboName", "comboName is required")
		return
	}

	combo, err := h.Repo.GetComboByName(comboName)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if combo == nil {
		writePlainError(w, http.StatusNotFound, "Combo not found")
		return
	}
	allCombos, err := h.Repo.GetCombos()
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, err.Error())
		return
	}

	targets := dedupeComboTestTargets(resolveComboTestTargets(combo, comboIndexByName(allCombos)))
	if len(targets) == 0 {
		writePlainError(w, http.StatusBadRequest, "Combo has no models")
		return
	}

	origin := handlerutil.RequestOrigin(r)
	apiKey := h.firstActiveAPIKey()
	client := &http.Client{Timeout: comboTestTimeout}
	results := h.runComboTestTargets(r.Context(), client, origin, apiKey, targets)

	resolved := firstOKComboTestResult(results)
	response := map[string]any{
		"comboName":              comboName,
		"strategy":               comboStrategyOrDefault(combo),
		"results":                results,
		"testedAt":               time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"resolvedBy":             nil,
		"resolvedByExecutionKey": nil,
		"resolvedByTarget":       nil,
	}
	if resolved != nil {
		response["resolvedBy"] = resolved.ModelStr
		response["resolvedByExecutionKey"] = resolved.ExecutionKey
		response["resolvedByTarget"] = map[string]any{
			"model":        resolved.ModelStr,
			"provider":     resolved.Provider,
			"stepId":       resolved.StepID,
			"executionKey": resolved.ExecutionKey,
			"connectionId": resolved.ConnectionID,
			"label":        resolved.Label,
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, response)
}

// writeComboTestValidation answers with the validation body Axon's test
// route builds: { error: { message, details: [{ field, message }] } }.
func writeComboTestValidation(w http.ResponseWriter, message, field, detail string) {
	handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
		"error": map[string]any{
			"message": message,
			"details": []map[string]string{{"field": field, "message": detail}},
		},
	})
}

// comboStrategyOrDefault mirrors Axon's `combo.strategy || "priority"` for
// an unresolved strategy. GetComboByName already resolves one (default
// "fallback"), so this only fires for a combo read that bypassed resolution.
func comboStrategyOrDefault(combo *models.Combo) string {
	if combo.Strategy != "" {
		return combo.Strategy
	}
	return "priority"
}

func comboIndexByName(combos []*models.Combo) map[string]*models.Combo {
	index := make(map[string]*models.Combo, len(combos))
	for _, combo := range combos {
		if combo != nil && combo.Name != "" {
			index[combo.Name] = combo
		}
	}
	return index
}

// resolveComboTestTargets flattens a combo's members into test targets,
// expanding members that name another combo (Axon's combo-ref steps) with a
// visited trail against cycles and the shared max depth.
func resolveComboTestTargets(combo *models.Combo, byName map[string]*models.Combo) []comboTestTarget {
	return resolveComboTestTargetsAt(combo, byName, 0, nil)
}

func resolveComboTestTargetsAt(combo *models.Combo, byName map[string]*models.Combo, depth int, trail []string) []comboTestTarget {
	if combo == nil || depth >= comboTestMaxDepth {
		return nil
	}
	members, err := comboModels(combo.Models)
	if err != nil {
		return nil
	}
	var targets []comboTestTarget
	for _, member := range members {
		member = strings.TrimSpace(member)
		if member == "" {
			continue
		}
		if nested, ok := byName[member]; ok && !containsString(trail, member) {
			targets = append(targets, resolveComboTestTargetsAt(
				nested, byName, depth+1, append(append([]string{}, trail...), member),
			)...)
			continue
		}
		provider := "unknown"
		if slash := strings.Index(member, "/"); slash > 0 {
			provider = member[:slash]
		}
		targets = append(targets, comboTestTarget{
			ModelStr:     member,
			Provider:     provider,
			ExecutionKey: member,
		})
	}
	return targets
}

// dedupeComboTestTargets drops repeats by execution key, first occurrence
// winning — Axon dedups the same way so a diamond-shaped combo tree does not
// test one model twice.
func dedupeComboTestTargets(targets []comboTestTarget) []comboTestTarget {
	seen := make(map[string]bool, len(targets))
	out := make([]comboTestTarget, 0, len(targets))
	for _, target := range targets {
		if seen[target.ExecutionKey] {
			continue
		}
		seen[target.ExecutionKey] = true
		out = append(out, target)
	}
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func firstOKComboTestResult(results []comboTestResult) *comboTestResult {
	for i := range results {
		if results[i].Status == "ok" {
			return &results[i]
		}
	}
	return nil
}

// runComboTestTargets executes the health checks in batches of
// comboTestConcurrency, preserving target order in the results.
func (h *DashboardHandler) runComboTestTargets(ctx context.Context, client *http.Client, origin, apiKey string, targets []comboTestTarget) []comboTestResult {
	results := make([]comboTestResult, len(targets))
	for start := 0; start < len(targets); start += comboTestConcurrency {
		end := start + comboTestConcurrency
		if end > len(targets) {
			end = len(targets)
		}
		var wg sync.WaitGroup
		for i := start; i < end; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				results[i] = runComboTestTarget(ctx, client, origin, apiKey, targets[i])
			}(i)
		}
		wg.Wait()
	}
	return results
}

// runComboTestTarget sends the one health-check request for a target through
// the gateway's own /v1/chat/completions, with Axon's exact probe body and
// headers.
func runComboTestTarget(ctx context.Context, client *http.Client, origin, apiKey string, target comboTestTarget) comboTestResult {
	start := time.Now()
	result := comboTestResult{comboTestTarget: target}

	payload, _ := json.Marshal(map[string]any{
		"model":  target.ModelStr,
		"stream": false,
		"messages": []map[string]string{
			{"role": "system", "content": "You are a health check. Reply with a very short plain-text acknowledgement."},
			{"role": "user", "content": "Reply with: combo test ok"},
		},
		"max_tokens":  24,
		"temperature": 0,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		result.Status = "error"
		result.Error = err.Error()
		result.LatencyMs = time.Since(start).Milliseconds()
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("X-Internal-Test", "combo-health-check")
	req.Header.Set("X-OmniRouter-No-Cache", "true")
	req.Header.Set("X-Request-Id", "combo-test-"+uuid.NewString())

	resp, err := client.Do(req)
	result.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		result.Status = "error"
		if isTimeoutError(err) {
			result.Error = "Timeout (20s)"
		} else {
			result.Error = err.Error()
		}
		return result
	}
	defer resp.Body.Close()
	result.StatusCode = &resp.StatusCode

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	var parsed map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &parsed)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Status = "error"
		result.Error = chatErrorMessage(parsed, resp.Status)
		return result
	}
	text := extractChatResponseText(parsed)
	if text == "" {
		result.Status = "error"
		result.Error = "Provider returned HTTP 200 but no text content."
		return result
	}
	result.Status = "ok"
	result.ResponseText = text
	return result
}

// isTimeoutError reports whether a client failure was a deadline, which
// Axon's AbortError branch renders as "Timeout (20s)".
func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// chatErrorMessage pulls an error message out of a chat error body — the
// error field as an object with a message, or as a bare string — falling
// back to the HTTP status text, as Axon's test does.
func chatErrorMessage(body map[string]any, status string) string {
	if body != nil {
		switch errValue := body["error"].(type) {
		case map[string]any:
			if message, ok := errValue["message"].(string); ok && message != "" {
				return message
			}
		case string:
			if errValue != "" {
				return errValue
			}
		}
	}
	return status
}

// extractChatResponseText reads choices[0].message.content from a chat
// completion, accepting a plain string or text parts.
func extractChatResponseText(body map[string]any) string {
	if body == nil {
		return ""
	}
	choices, _ := body["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	first, _ := choices[0].(map[string]any)
	message, _ := first["message"].(map[string]any)
	switch content := message["content"].(type) {
	case string:
		return strings.TrimSpace(content)
	case []any:
		var parts []string
		for _, part := range content {
			if record, ok := part.(map[string]any); ok {
				if text, ok := record["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	}
	return ""
}

// firstActiveAPIKey returns the first active dashboard API key, the
// credential Axon's test sends as its internal Bearer (its
// getCurrentActiveApiKey). Empty when no active key exists — the probe then
// goes out unauthenticated, as Axon's does when its lookup fails.
func (h *DashboardHandler) firstActiveAPIKey() string {
	keys, err := h.Repo.GetApiKeys()
	if err != nil {
		return ""
	}
	for _, key := range keys {
		if key != nil && key.IsActive == 1 && key.Key != "" {
			return key.Key
		}
	}
	return ""
}
