package providers

import (
	"net/http"
	"testing"
)

func TestClassifyMinimaxCode401AuthFailed(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		statusCode     int
		errorText      string
		wantDeactivate bool
		wantFallback   bool
	}{
		{
			name:           "minimax-code-global 401 auth failed deactivates key",
			provider:       "minimax-code-global",
			statusCode:     http.StatusUnauthorized,
			errorText:      `{"code":401,"message":"auth failed"}`,
			wantDeactivate: true,
			wantFallback:   true,
		},
		{
			name:           "mmg alias 401 auth failed deactivates key",
			provider:       "mmg",
			statusCode:     http.StatusUnauthorized,
			errorText:      `auth failed`,
			wantDeactivate: true,
			wantFallback:   true,
		},
		{
			name:           "minimax-code 401 auth failed deactivates key",
			provider:       "minimax-code",
			statusCode:     http.StatusUnauthorized,
			errorText:      `{"code":401,"message":"auth failed"}`,
			wantDeactivate: true,
			wantFallback:   true,
		},
		{
			name:           "mmc alias 401 unauthorized deactivates key",
			provider:       "mmc",
			statusCode:     http.StatusUnauthorized,
			errorText:      `Unauthorized`,
			wantDeactivate: true,
			wantFallback:   true,
		},
		{
			name:           "minimax-code-global 429 credit limit does not deactivate",
			provider:       "minimax-code-global",
			statusCode:     http.StatusTooManyRequests,
			errorText:      `usage limit reached: 余额不足`,
			wantDeactivate: false,
			wantFallback:   true,
		},
		{
			name:           "minimax-code-global 500 server error does not deactivate",
			provider:       "minimax-code-global",
			statusCode:     http.StatusInternalServerError,
			errorText:      `internal server error`,
			wantDeactivate: false,
			wantFallback:   true,
		},
		{
			name:           "other provider 401 auth failed is not claimed by minimax-code rule",
			provider:       "openai",
			statusCode:     http.StatusUnauthorized,
			errorText:      `{"code":401,"message":"auth failed"}`,
			wantDeactivate: false,
			wantFallback:   true,
		},
		{
			name:           "minimax api-key provider is isolated from minimax-code rule",
			provider:       "minimax",
			statusCode:     http.StatusUnauthorized,
			errorText:      `{"code":401,"message":"auth failed"}`,
			wantDeactivate: false,
			wantFallback:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyErrorForProvider(tt.statusCode, tt.errorText, 0, tt.provider)
			if got.DeactivateKey != tt.wantDeactivate {
				t.Errorf("ClassifyErrorForProvider(%d, %q, %q).DeactivateKey = %v, want %v",
					tt.statusCode, tt.errorText, tt.provider, got.DeactivateKey, tt.wantDeactivate)
			}
			if got.ShouldFallback != tt.wantFallback {
				t.Errorf("ClassifyErrorForProvider(%d, %q, %q).ShouldFallback = %v, want %v",
					tt.statusCode, tt.errorText, tt.provider, got.ShouldFallback, tt.wantFallback)
			}
		})
	}
}

func TestClassifyMinimaxCodeCreditExhausted(t *testing.T) {
	tests := []struct {
		name          string
		provider      string
		statusCode    int
		errorText     string
		wantExhausted bool
		wantCooldown  int
	}{
		{
			name:          "mmg normalized 429 usage limit reached parks 2d exhausted",
			provider:      "mmg",
			statusCode:    http.StatusTooManyRequests,
			errorText:     `usage limit reached: 余额不足`,
			wantExhausted: true,
			wantCooldown:  minimaxCodeCreditExhaustedCooldownMs,
		},
		{
			name:          "minimax-code-global 429 CJK points exhausted parks 2d",
			provider:      "minimax-code-global",
			statusCode:    http.StatusTooManyRequests,
			errorText:     `积分不足`,
			wantExhausted: true,
			wantCooldown:  minimaxCodeCreditExhaustedCooldownMs,
		},
		{
			name:          "mmc 402 insufficient balance parks 2d exhausted",
			provider:      "mmc",
			statusCode:    http.StatusPaymentRequired,
			errorText:     `insufficient balance`,
			wantExhausted: true,
			wantCooldown:  minimaxCodeCreditExhaustedCooldownMs,
		},
		{
			name:          "mmg 400 not enough credits errorCode 50110 parks 2d exhausted",
			provider:      "mmg",
			statusCode:    http.StatusBadRequest,
			errorText:     `{"error":"not enough credits","errorCode":50110}`,
			wantExhausted: true,
			wantCooldown:  minimaxCodeCreditExhaustedCooldownMs,
		},
		{
			name:          "mmg generic 429 throttle keeps exponential backoff",
			provider:      "mmg",
			statusCode:    http.StatusTooManyRequests,
			errorText:     `too many requests`,
			wantExhausted: false,
			wantCooldown:  BackoffConfig.BaseMs,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyErrorForProvider(tt.statusCode, tt.errorText, 0, tt.provider)
			if got.DeactivateKey {
				t.Errorf("credit exhaustion must not deactivate key permanently")
			}
			if !got.ShouldFallback {
				t.Errorf("ShouldFallback = false, want true")
			}
			if got.Exhausted != tt.wantExhausted {
				t.Errorf("Exhausted = %v, want %v", got.Exhausted, tt.wantExhausted)
			}
			if got.CooldownMs != tt.wantCooldown {
				t.Errorf("CooldownMs = %d, want %d", got.CooldownMs, tt.wantCooldown)
			}
		})
	}
}

func TestLooksLikePermanentFailure_AuthFailed(t *testing.T) {
	if !LooksLikePermanentFailure(`{"code":401,"message":"auth failed"}`) {
		t.Fatal("LooksLikePermanentFailure returned false for MiniMax auth failed payload, want true")
	}
}

func TestLooksLikeQuotaExhausted_MiniMax(t *testing.T) {
	for _, sample := range []string{
		`usage limit reached: 余额不足`,
		`积分不足`,
		`额度已用完`,
		`{"error":"not enough credits","errorCode":50110}`,
	} {
		if !LooksLikeQuotaExhausted(sample) {
			t.Errorf("LooksLikeQuotaExhausted(%q) = false, want true", sample)
		}
	}
}
