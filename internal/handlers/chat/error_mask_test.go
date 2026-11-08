package chat

import (
	"net/http"
	"strings"
	"testing"
)

func TestMaskClientFacingError_429(t *testing.T) {
	rawBody := []byte(`{"error":{"message":"Rate limit exceeded","code":"rate_limit_exceeded"}}`)
	masked := MaskClientFacingError(http.StatusTooManyRequests, rawBody, "60", false)

	if masked.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", masked.StatusCode)
	}
	if masked.RetryAfter != "2" {
		t.Errorf("expected Retry-After clamped to 2, got %s", masked.RetryAfter)
	}
	if !strings.Contains(string(masked.Body), "upstream_unavailable") {
		t.Errorf("expected upstream_unavailable code in body, got %s", string(masked.Body))
	}
}

func TestMaskClientFacingError_402_403(t *testing.T) {
	raw402 := []byte(`{"error":{"message":"insufficient quota"}}`)
	masked402 := MaskClientFacingError(http.StatusPaymentRequired, raw402, "", false)
	if masked402.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for 402, got %d", masked402.StatusCode)
	}

	raw403 := []byte(`{"error":{"message":"quota exceeded"}}`)
	masked403 := MaskClientFacingError(http.StatusForbidden, raw403, "", false)
	if masked403.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for 403, got %d", masked403.StatusCode)
	}
}

func TestMaskClientFacingError_Cloudflare524(t *testing.T) {
	htmlBody := []byte(`<!DOCTYPE html><html><head><title>524 A timeout occurred</title></head><body><h1>Error</h1></body></html>`)
	masked := MaskClientFacingError(524, htmlBody, "", false)

	if masked.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("expected 504 for Cloudflare 524, got %d", masked.StatusCode)
	}
	if !strings.Contains(string(masked.Body), "gateway_error") {
		t.Errorf("expected gateway_error in body, got %s", string(masked.Body))
	}
}

func TestMaskClientFacingError_HTMLBody(t *testing.T) {
	htmlBody := []byte(`<html><body>Bad Gateway</body></html>`)
	masked := MaskClientFacingError(http.StatusBadGateway, htmlBody, "", false)

	if masked.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", masked.StatusCode)
	}
	if !strings.Contains(string(masked.Body), "upstream_html_response") {
		t.Errorf("expected JSON response for HTML body, got %s", string(masked.Body))
	}
}

func TestMaskClientFacingError_Combo404(t *testing.T) {
	rawBody := []byte(`{"error":{"message":"model not found"}}`)
	maskedCombo := MaskClientFacingError(http.StatusNotFound, rawBody, "", true)
	if maskedCombo.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 for combo 404, got %d", maskedCombo.StatusCode)
	}

	maskedDirect := MaskClientFacingError(http.StatusNotFound, rawBody, "", false)
	if maskedDirect.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 preserved for direct model 404, got %d", maskedDirect.StatusCode)
	}
}
