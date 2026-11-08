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

	t.Run("Hourly Key Rate Limit locks Account with 70min floor (not ModelScoped)", func(t *testing.T) {
		body := `{"error":{"message":"Rate limit reached. You can send your next request in 59 min 1 s.","type":"rate_limit_exceeded"}}`
		cls, ok := classifyTokenHarbor(http.StatusTooManyRequests, body, 0, "tokenharbor")
		if !ok {
			t.Fatal("expected classification to match")
		}
		if cls.ModelScoped {
			t.Error("expected ModelScoped to be FALSE for account-level key throttle")
		}
		expectedMs := int(70 * 60 * 1000) // 70 min floor
		if cls.CooldownMs != expectedMs {
			t.Errorf("expected %dms (70m floor), got %dms", expectedMs, cls.CooldownMs)
		}
	})

	t.Run("Short burst rate limit gets 5s buffer", func(t *testing.T) {
		body := `{"error":{"message":"Rate limit reached. You can send your next request in 32 s.","type":"rate_limit_exceeded"}}`
		cls, ok := classifyTokenHarbor(http.StatusTooManyRequests, body, 0, "tokenharbor")
		if !ok {
			t.Fatal("expected classification to match")
		}
		if cls.ModelScoped {
			t.Error("expected ModelScoped to be FALSE for short burst limit")
		}
		expectedMs := int((32 + 5) * 1000) // 32s + 5s buffer = 37s
		if cls.CooldownMs != expectedMs {
			t.Errorf("expected %dms (37s), got %dms", expectedMs, cls.CooldownMs)
		}
	})

	t.Run("Default hourly rate limit without duration gets 70min floor", func(t *testing.T) {
		body := `{"error":{"message":"Rate limit reached.","type":"rate_limit_exceeded"}}`
		cls, ok := classifyTokenHarbor(http.StatusTooManyRequests, body, 0, "tokenharbor")
		if !ok {
			t.Fatal("expected classification to match")
		}
		expectedMs := int(70 * 60 * 1000) // 70 min floor
		if cls.CooldownMs != expectedMs {
			t.Errorf("expected %dms (70m floor), got %dms", expectedMs, cls.CooldownMs)
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

	t.Run("Upstream HTML 500 error gives short transient cooldown", func(t *testing.T) {
		body := `upstream returned HTML: 500: This page couldn’t load`
		cls, ok := classifyTokenHarbor(http.StatusInternalServerError, body, 0, "tokenharbor")
		if !ok {
			t.Fatal("expected classification to match")
		}
		if cls.ModelScoped {
			t.Error("expected ModelScoped to be FALSE for 500 transient error")
		}
		if cls.CooldownMs != 15*1000 {
			t.Errorf("expected 15s cooldown, got %dms", cls.CooldownMs)
		}
	})

	t.Run("Upstream server_busy under heavy load gives 5min account cooldown", func(t *testing.T) {
		body := `{"error":{"message":"Token Harbor is under heavy load. Requests from this account are being slowed down for a few minutes; please retry shortly.","type":"rate_limit_error","code":"server_busy"}}`
		cls, ok := classifyTokenHarbor(http.StatusTooManyRequests, body, 0, "tokenharbor")
		if !ok {
			t.Fatal("expected classification to match")
		}
		if cls.CooldownMs != 5*60*1000 {
			t.Errorf("expected 5min cooldown (300000ms), got %dms", cls.CooldownMs)
		}
	})
}
