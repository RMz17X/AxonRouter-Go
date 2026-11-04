package oauth

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/proxy/executor"
)

// Freebuff probe port (AxonRouter src/lib/freebuff/probe.ts): the
// codebuff.com session/agent-run/completion calls behind the freebuff
// import + realtest routes. Transport goes through the executor's freebuff
// client so proxy refusal fallback matches every other freebuff call in
// this codebase.
const (
	freebuffProbeBaseURL    = "https://www.codebuff.com"
	freebuffDefaultModel    = "deepseek/deepseek-v4-flash"
	freebuffDefaultAgentID  = "base2-free-deepseek-flash"
	freebuffDefaultClientID = "axonrouter-freebuff-probe"
	freebuffProbeUA         = "ai-sdk/openai-compatible/0.0.96/codebuff-freebuff"
)

// freebuffProbeResult is one upstream probe call: HTTP status + decoded JSON
// payload (nil when the body was not a JSON object).
type freebuffProbeResult struct {
	status int
	data   map[string]any
}

func freebuffProbeCall(ctx context.Context, method, path, token, instanceID string, payload any) (*freebuffProbeResult, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, freebuffProbeBaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", freebuffProbeUA)
	if strings.TrimSpace(instanceID) != "" {
		req.Header.Set("X-Freebuff-Instance-Id", strings.TrimSpace(instanceID))
	}
	resp, err := executor.DoFreebuffHTTP(ctx, &http.Client{Timeout: 30 * time.Second}, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	out := &freebuffProbeResult{status: resp.StatusCode}
	var data map[string]any
	if len(raw) > 0 && json.Unmarshal(raw, &data) == nil {
		out.data = data
	}
	return out, nil
}

// getFreebuffProbeSession mirrors getFreebuffSession (GET session).
func getFreebuffProbeSession(ctx context.Context, token string) (*freebuffProbeResult, error) {
	return freebuffProbeCall(ctx, http.MethodGet, "/api/v1/freebuff/session", token, "", nil)
}

// joinFreebuffProbeSession mirrors joinFreebuffSession (POST session {model}).
func joinFreebuffProbeSession(ctx context.Context, token, model string) (*freebuffProbeResult, error) {
	if model == "" {
		model = freebuffDefaultModel
	}
	return freebuffProbeCall(ctx, http.MethodPost, "/api/v1/freebuff/session", token, "", map[string]any{"model": model})
}

// freebuffSessionActive mirrors isFreebuffSessionActive.
func freebuffSessionActive(data map[string]any) bool {
	if data == nil {
		return false
	}
	if s, _ := data["status"].(string); s != "active" {
		return false
	}
	if ms, ok := data["remainingMs"].(float64); ok && ms <= 0 {
		return false
	}
	if exp, ok := data["expiresAt"].(string); ok && exp != "" {
		if t, err := time.Parse(time.RFC3339, exp); err == nil && !t.After(time.Now()) {
			return false
		}
	}
	return true
}

// freebuffEnsure is ensureFreebuffSession's outcome: the GET session result
// (nil on the force-join path) and the join result (nil when not attempted).
type freebuffEnsure struct {
	active  bool
	session *freebuffProbeResult
	join    *freebuffProbeResult
}

func ensureFreebuffProbeSession(ctx context.Context, token, model string, forceJoin bool) (*freebuffEnsure, error) {
	if forceJoin {
		join, err := joinFreebuffProbeSession(ctx, token, model)
		if err != nil {
			return nil, err
		}
		return &freebuffEnsure{active: freebuffSessionActive(join.data), join: join}, nil
	}
	session, err := getFreebuffProbeSession(ctx, token)
	if err != nil {
		return nil, err
	}
	if freebuffSessionActive(session.data) {
		return &freebuffEnsure{active: true, session: session}, nil
	}
	switch s, _ := session.data["status"].(string); s {
	case "rate_limited", "country_blocked", "banned":
		return &freebuffEnsure{active: false, session: session}, nil
	}
	join, err := joinFreebuffProbeSession(ctx, token, model)
	if err != nil {
		return nil, err
	}
	return &freebuffEnsure{active: freebuffSessionActive(join.data), session: session, join: join}, nil
}

// freebuffFingerprint mirrors extractFreebuffFingerprint (session.instanceId).
func freebuffFingerprint(data map[string]any) string {
	if data == nil {
		return ""
	}
	if s, ok := data["instanceId"].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// explainFreebuffError mirrors explainFreebuffError's fixed interpretations.
func explainFreebuffError(data map[string]any) any {
	if data == nil {
		return nil
	}
	switch code, _ := data["error"].(string); code {
	case "free_mode_invalid_agent_model":
		return "Agent/model combo is invalid for free mode."
	case "freebuff_update_required":
		return "Runtime/session state is not accepted for free mode; restart freebuff and re-check session admission."
	case "No runId found in request body":
		return "Completion payload is missing codebuff_metadata.run_id."
	}
	if s, _ := data["status"].(string); s == "rate_limited" {
		return "Freebuff daily session limit is exhausted until resetAt."
	}
	return nil
}
