package chat

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// MaskedClientError holds the sanitized HTTP status code, response body, and
// optional Retry-After header safe to return to CLI agents (Hermes, OpenCode,
// Claude Code, Cursor) without triggering client-side model blacklisting or quota aborts.
type MaskedClientError struct {
	StatusCode int
	Body       []byte
	RetryAfter string
}

// isHTMLBody checks if a response body appears to be HTML (e.g. Cloudflare or Nginx error pages).
func isHTMLBody(b []byte) bool {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 {
		return false
	}
	if trimmed[0] == '<' {
		return true
	}
	lower := bytes.ToLower(trimmed)
	return bytes.Contains(lower, []byte("<!doctype html")) || bytes.Contains(lower, []byte("<html"))
}

// sanitizeErrorMessage extracts an informative string from an upstream error body or returns a default.
func sanitizeErrorMessage(body []byte, defaultMsg string) string {
	if len(body) == 0 || isHTMLBody(body) {
		return defaultMsg
	}
	var doc struct {
		Error any `json:"error"`
	}
	if err := json.Unmarshal(body, &doc); err == nil && doc.Error != nil {
		switch e := doc.Error.(type) {
		case string:
			if e != "" {
				return e
			}
		case map[string]any:
			if msg, ok := e["message"].(string); ok && msg != "" {
				return msg
			}
		}
	}
	return defaultMsg
}

// MaskClientFacingError transforms upstream failure codes and bodies into client-friendly,
// retryable server errors so that CLI tools perform automatic retries rather than blacklisting models.
func MaskClientFacingError(statusCode int, body []byte, rawRetryAfter string, isCombo bool) MaskedClientError {
	maskedStatus := statusCode
	maskedRetry := ""

	// 1. Cloudflare custom 5xx status codes (520-526)
	if statusCode >= 520 && statusCode <= 526 {
		if statusCode == 524 {
			maskedStatus = http.StatusGatewayTimeout // 504
		} else {
			maskedStatus = http.StatusBadGateway // 502
		}
		jsonBody, _ := json.Marshal(map[string]any{
			"error": map[string]any{
				"message": fmt.Sprintf("Upstream gateway error (Cloudflare %d), please retry", statusCode),
				"type":    "server_error",
				"code":    "gateway_error",
			},
		})
		return MaskedClientError{
			StatusCode: maskedStatus,
			Body:       jsonBody,
			RetryAfter: "1",
		}
	}

	// 2. HTTP 429 Too Many Requests:
	// Relay as 503 Service Unavailable so clients (Hermes, OpenCode) treat it as a transient
	// server overload and auto-retry without classifying it as account-exhausted rate-limit.
	if statusCode == http.StatusTooManyRequests {
		maskedStatus = http.StatusServiceUnavailable
		// Clamp Retry-After to max 2s so client doesn't freeze for minutes
		if rawRetryAfter != "" {
			if sec, err := strconv.Atoi(strings.TrimSpace(rawRetryAfter)); err == nil && sec > 2 {
				maskedRetry = "2"
			} else {
				maskedRetry = rawRetryAfter
			}
		} else {
			maskedRetry = "1"
		}
		jsonBody, _ := json.Marshal(map[string]any{
			"error": map[string]any{
				"message": "Upstream service temporarily busy, please retry",
				"type":    "server_error",
				"code":    "upstream_unavailable",
			},
		})
		return MaskedClientError{
			StatusCode: maskedStatus,
			Body:       jsonBody,
			RetryAfter: maskedRetry,
		}
	}

	// 3. HTTP 402 Payment Required or 403 Forbidden (insufficient balance / quota wall / WAF block):
	// Relay as 503 so client tools don't assume the client's own key is out of money.
	if statusCode == http.StatusPaymentRequired || statusCode == http.StatusForbidden {
		maskedStatus = http.StatusServiceUnavailable
		jsonBody, _ := json.Marshal(map[string]any{
			"error": map[string]any{
				"message": "Upstream capacity temporarily exhausted, please retry",
				"type":    "server_error",
				"code":    "upstream_unavailable",
			},
		})
		return MaskedClientError{
			StatusCode: maskedStatus,
			Body:       jsonBody,
			RetryAfter: "2",
		}
	}

	// 4. HTTP 404 Not Found on combos:
	// If a specific combo member was 404 upstream, returning 404 to client makes it think
	// the virtual combo model name itself doesn't exist on AxonRouter.
	if statusCode == http.StatusNotFound && isCombo {
		maskedStatus = http.StatusBadGateway
		jsonBody, _ := json.Marshal(map[string]any{
			"error": map[string]any{
				"message": "All combo upstream models failed (last: upstream model not found)",
				"type":    "server_error",
				"code":    "upstream_model_not_found",
			},
		})
		return MaskedClientError{
			StatusCode: maskedStatus,
			Body:       jsonBody,
		}
	}

	// 5. HTML error bodies on any 4xx/5xx:
	// Always normalize HTML into OpenAI-compatible JSON to prevent client JSON parser crashes.
	if isHTMLBody(body) {
		statusText := http.StatusText(statusCode)
		if statusText == "" {
			statusText = "server error"
		}
		jsonBody, _ := json.Marshal(map[string]any{
			"error": map[string]any{
				"message": fmt.Sprintf("Upstream HTTP %d (%s)", statusCode, statusText),
				"type":    "server_error",
				"code":    "upstream_html_response",
			},
		})
		return MaskedClientError{
			StatusCode: maskedStatus,
			Body:       jsonBody,
			RetryAfter: rawRetryAfter,
		}
	}

	return MaskedClientError{
		StatusCode: maskedStatus,
		Body:       body,
		RetryAfter: rawRetryAfter,
	}
}
