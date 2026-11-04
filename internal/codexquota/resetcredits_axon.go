package codexquota

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Axon-flavoured reset-credit calls — a direct port of the service functions
// behind AxonRouter's GET/POST /api/usage/[connectionId]/codex-reset-credits
// (open-sse/services/usage/codex.js: getCodexRateLimitResetCredits and
// consumeCodexRateLimitResetCredit). They deliberately do NOT reuse the
// OmniRoute-shaped ListResetCredits/ConsumeResetCredit above: Axon's wire
// contract differs — no credit selection, no availability filtering, its own
// header set, and a consume body carrying only the redeem request id — and
// the Axon dashboard endpoint depends on those exact shapes.

// AxonResetCredit is one credit as Axon's list response renders it. Dates are
// ISO strings or nil, matching toIsoDate's null.
type AxonResetCredit struct {
	Status    string  `json:"status"`
	GrantedAt *string `json:"grantedAt"`
	ExpiresAt *string `json:"expiresAt"`
}

// AxonResetCredits is the parsed list response.
type AxonResetCredits struct {
	AvailableCount int               `json:"availableCount"`
	Credits        []AxonResetCredit `json:"credits"`
}

// AxonConsumeResult mirrors the object Axon's consume service returns: HTTP
// failures are results, not errors — only transport/parse failures and a
// missing token or redeem id raise.
type AxonConsumeResult struct {
	OK           bool
	NoCredit     bool
	Status       int
	Code         *string
	WindowsReset float64
	Message      *string
	Raw          map[string]any
}

// axonISODate normalises a wham timestamp the way Axon's toIsoDate does: a
// number is epoch seconds (or milliseconds when too large for seconds), a
// string is parsed as a date; anything unparseable becomes nil.
func axonISODate(value any) *string {
	format := func(t time.Time) *string {
		s := t.UTC().Format("2006-01-02T15:04:05.000Z")
		return &s
	}
	switch v := value.(type) {
	case float64:
		if v == 0 {
			return nil
		}
		if v < 1e12 {
			return format(time.Unix(int64(v), int64((v-float64(int64(v)))*1e9)))
		}
		return format(time.UnixMilli(int64(v)))
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil
		}
		if parsed, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
			return format(parsed)
		}
		if epoch, err := strconv.ParseFloat(trimmed, 64); err == nil && epoch != 0 {
			return axonISODate(epoch)
		}
	}
	return nil
}

// axonErrorMessage renders an upstream error payload the way Axon's
// errorMessage does: a string as-is, an object by its message field, anything
// else as JSON.
func axonErrorMessage(value any, fallback string) string {
	switch v := value.(type) {
	case nil:
		return fallback
	case string:
		if v != "" {
			return v
		}
	case map[string]any:
		if message, ok := v["message"].(string); ok && message != "" {
			return message
		}
		if encoded, err := json.Marshal(v); err == nil {
			return string(encoded)
		}
	}
	return fallback
}

// axonFiniteNumber is toFiniteNumber for the fields Axon reads numerically:
// numbers pass through, numeric strings parse, everything else is 0.
func axonFiniteNumber(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return parsed
		}
	}
	return 0
}

// axonWhamCall performs one wham round trip with an explicit header set —
// Axon's list and consume calls send different headers, so the shared
// resetCreditHeaders helper (which always sends the account id and a
// content type) cannot stand in for either.
func axonWhamCall(
	ctx context.Context,
	client *http.Client,
	method, url string,
	headers map[string]string,
	body []byte,
) (int, map[string]any, error) {
	if client == nil {
		client = defaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("codexquota: build request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		if proxyRefused(err) {
			return 0, nil, &ProxyRefusedError{URL: url, Err: err}
		}
		return 0, nil, fmt.Errorf("codexquota: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResetCreditBodyBytes))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("codexquota: read body: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return resp.StatusCode, nil, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return resp.StatusCode, nil, nil
	}
	return resp.StatusCode, payload, nil
}

// FetchAxonResetCredits lists an account's reset credits, Axon-style: every
// credit the upstream returns, unfiltered, with dates normalised.
func FetchAxonResetCredits(
	ctx context.Context,
	client *http.Client,
	accessToken, accountID string,
) (AxonResetCredits, error) {
	if accessToken == "" {
		return AxonResetCredits{}, errors.New("No Codex access token available. Please re-authorize the connection.")
	}
	headers := map[string]string{
		"Authorization": "Bearer " + accessToken,
		"Accept":        "application/json",
		"OpenAI-Beta":   "codex-1",
		"originator":    "codex_cli_rs",
	}
	if accountID != "" {
		headers["ChatGPT-Account-ID"] = accountID
	}

	status, data, err := axonWhamCall(ctx, client, http.MethodGet, ResetCreditsURL, headers, nil)
	if err != nil {
		return AxonResetCredits{}, err
	}
	if status < 200 || status >= 300 {
		fallback := fmt.Sprintf("Codex reset credits API unavailable (%d).", status)
		message := fallback
		if data != nil {
			// Axon reads data.message || data.error || data.detail — the
			// first truthy value, rendered by errorMessage.
			for _, key := range []string{"message", "error", "detail"} {
				if rendered := axonErrorMessage(data[key], ""); rendered != "" {
					message = rendered
					break
				}
			}
		}
		return AxonResetCredits{}, errors.New(message)
	}

	result := AxonResetCredits{Credits: []AxonResetCredit{}}
	if data == nil {
		return result, nil
	}
	if count := axonFiniteNumber(data["available_count"]); data["available_count"] != nil {
		if count > 0 {
			result.AvailableCount = int(count)
		}
	} else if count := axonFiniteNumber(data["availableCount"]); data["availableCount"] != nil && count > 0 {
		result.AvailableCount = int(count)
	}
	if rawCredits, ok := data["credits"].([]any); ok {
		for _, item := range rawCredits {
			credit := AxonResetCredit{Status: "unknown"}
			if record, ok := item.(map[string]any); ok {
				if s, ok := record["status"].(string); ok && s != "" {
					credit.Status = s
				}
				credit.GrantedAt = axonISODate(firstPresent(record, "granted_at", "grantedAt"))
				credit.ExpiresAt = axonISODate(firstPresent(record, "expires_at", "expiresAt"))
			}
			result.Credits = append(result.Credits, credit)
		}
	}
	return result, nil
}

// firstPresent returns the first key present in the record, even when its
// value is null — Axon's ?? chain treats an explicit null as absent too, but
// a present-null and a missing key both end at toIsoDate(null) = null, so the
// distinction is invisible here.
func firstPresent(record map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, present := record[key]; present {
			return value
		}
	}
	return nil
}

// ConsumeAxonResetCredit redeems one credit by redeem request id, exactly as
// Axon's consume does: no credit id, no account header.
func ConsumeAxonResetCredit(
	ctx context.Context,
	client *http.Client,
	accessToken, redeemRequestID string,
) (AxonConsumeResult, error) {
	if accessToken == "" {
		return AxonConsumeResult{}, errors.New("No Codex access token available. Please re-authorize the connection.")
	}
	if redeemRequestID == "" {
		return AxonConsumeResult{}, errors.New("A redeem request id is required to consume a Codex reset credit.")
	}

	body, _ := json.Marshal(map[string]string{"redeem_request_id": redeemRequestID})
	headers := map[string]string{
		"Authorization": "Bearer " + accessToken,
		"Accept":        "application/json",
		"Content-Type":  "application/json",
	}
	status, data, err := axonWhamCall(ctx, client, http.MethodPost, ConsumeResetCreditsURL, headers, body)
	if err != nil {
		return AxonConsumeResult{}, fmt.Errorf("Failed to consume Codex reset credit: %w", err)
	}

	result := AxonConsumeResult{Status: status, Raw: data}
	if data != nil {
		if code, ok := data["code"].(string); ok && code != "" {
			result.Code = &code
		}
		if message, ok := data["message"].(string); ok && message != "" {
			result.Message = &message
		}
		result.WindowsReset = axonFiniteNumber(data["windows_reset"])
	}
	ok := status >= 200 && status < 300
	result.OK = ok && ((result.Code != nil && *result.Code == "reset") || result.WindowsReset > 0)
	result.NoCredit = ok && result.Code != nil && *result.Code == "no_credit"
	return result, nil
}
