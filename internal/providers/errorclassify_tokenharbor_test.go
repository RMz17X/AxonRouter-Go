package providers

import (
	"net/http"
	"testing"
	"time"
)

func TestParseTokenHarborRetryDuration(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected time.Duration
		ok       bool
	}{
		{
			name:     "59 min 1 s",
			input:    `{"error":{"message":"Rate limit reached. You can send your next request in 59 min 1 s.","type":"rate_limit_exceeded"}}`,
			expected: (59*60 + 1) * time.Second,
			ok:       true,
		},
		{
			name:     "30 s",
			input:    `{"error":{"message":"Rate limit reached. You can send your next request in 30 s.","type":"rate_limit_exceeded"}}`,
			expected: 30 * time.Second,
			ok:       true,
		},
		{
			name:     "15 min",
			input:    `Rate limit reached. You can send your next request in 15 min.`,
			expected: 15 * 60 * time.Second,
			ok:       true,
		},
		{
			name:     "unrelated error",
			input:    `{"error":{"message":"Internal server error"}}`,
			expected: 0,
			ok:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dur, ok := ParseTokenHarborRetryDuration(tt.input)
			if ok != tt.ok {
				t.Fatalf("expected ok=%v, got %v", tt.ok, ok)
			}
			if dur != tt.expected {
				t.Fatalf("expected dur=%v, got %v", tt.expected, dur)
			}
		})
	}
}

func TestClassifyTokenHarbor_Scenarios(t *testing.T) {
	t.Run("Model Capacity Cooldown is ModelScoped", func(t *testing.T) {
		body := `{"error":{"message":"The model is at capacity for your account, retry in about 45 seconds."}}`
		cls, ok := classifyTokenHarbor(http.StatusTooManyRequests, body, 0, "tokenharbor")
		if !ok {
			t.Fatal("expected classification to match")
		}
		if !cls.ModelScoped {
			t.Error("expected ModelScoped to be TRUE for model capacity")
		}
		if cls.CooldownMs != 45*1000 {
			t.Errorf("expected 45s cooldown, got %dms", cls.CooldownMs)
		}
	})

	t.Run("Hourly Key Rate Limit locks Account (not ModelScoped)", func(t *testing.T) {
		body := `{"error":{"message":"Rate limit reached. You can send your next request in 59 min 1 s.","type":"rate_limit_exceeded"}}`
		cls, ok := classifyTokenHarbor(http.StatusTooManyRequests, body, 0, "tokenharbor")
		if !ok {
			t.Fatal("expected classification to match")
		}
		if cls.ModelScoped {
			t.Error("expected ModelScoped to be FALSE for account-level key throttle")
		}
		expectedMs := int((59*60 + 1) * 1000)
		if cls.CooldownMs != expectedMs {
			t.Errorf("expected %dms, got %dms", expectedMs, cls.CooldownMs)
		}
	})

	t.Run("Egress IP Rate Limit does not lock model", func(t *testing.T) {
		body := `{"error":{"message":"Rate limit reached: many free accounts are sharing this network address.","type":"rate_limit_exceeded"}}`
		cls, ok := classifyTokenHarbor(http.StatusTooManyRequests, body, 0, "tokenharbor")
		if !ok {
			t.Fatal("expected classification to match")
		}
		if cls.ModelScoped {
			t.Error("expected ModelScoped to be FALSE for egress IP limit")
		}
		if cls.CooldownMs != 5*60*1000 {
			t.Errorf("expected 5m cooldown, got %dms", cls.CooldownMs)
		}
	})
}
