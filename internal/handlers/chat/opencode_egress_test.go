package chat

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func freeTier429() *upstreamError {
	return &upstreamError{
		StatusCode: http.StatusTooManyRequests,
		Body:       []byte(`{"error":{"message":"Free usage limit reached. Subscribe to OpenCode Go to keep going","type":"FreeUsageLimitError","code":429}}`),
	}
}

func genuineRateLimit429() *upstreamError {
	return &upstreamError{
		StatusCode: http.StatusTooManyRequests,
		Body:       []byte(`{"error":{"message":"Rate limit reached for free tier","type":"rate_limit_error","code":429}}`),
	}
}

func TestOpencodeEgressParkLadder(t *testing.T) {
	t.Setenv("OPENCODE_EGRESS_PARK_SEC", "")
	pool := "test-" + t.Name()
	t.Cleanup(func() {
		opencodeEgressMu.Lock()
		delete(opencodeEgressEntries, opencodeEgressKey("opencode", pool))
		opencodeEgressMu.Unlock()
	})

	want := []time.Duration{
		60 * time.Second,
		600 * time.Second,
		1200 * time.Second,
		1800 * time.Second,
		2400 * time.Second,
		7200 * time.Second,
		7200 * time.Second,
	}
	for i, expected := range want {
		strike, parked := parkOpenCodeEgress("opencode", pool, freeTier429())
		if strike != min(i+1, opencodeEgressMaxStrike) {
			t.Errorf("call %d: strike = %d, want %d", i+1, strike, min(i+1, opencodeEgressMaxStrike))
		}
		if parked != expected {
			t.Errorf("call %d: parked for %v, want %v", i+1, parked, expected)
		}
	}
}

func TestOpencodeEgressParkEnvOverrideOnlyFirstStrike(t *testing.T) {
	t.Setenv("OPENCODE_EGRESS_PARK_SEC", "30")
	pool := "test-" + t.Name()
	t.Cleanup(func() {
		opencodeEgressMu.Lock()
		delete(opencodeEgressEntries, opencodeEgressKey("opencode", pool))
		opencodeEgressMu.Unlock()
	})

	strike, parked := parkOpenCodeEgress("opencode", pool, freeTier429())
	if strike != 1 || parked != 30*time.Second {
		t.Fatalf("first strike: strike=%d parked=%v, want 1/30s", strike, parked)
	}
	_, parked = parkOpenCodeEgress("opencode", pool, freeTier429())
	if parked != 600*time.Second {
		t.Errorf("second strike: parked for %v, want 10m (env override must not apply)", parked)
	}
}

func classifiedRefusal(t *testing.T, status int, body string) error {
	t.Helper()
	raw := &upstreamError{
		StatusCode: status,
		Body:       []byte(body),
	}
	err := opencodeClosureError("opencode", "space-bunny-free", raw)
	if err == raw {
		t.Fatalf("opencodeClosureError did not classify %q", body)
	}
	return err
}

func TestPublishOpenCodeGateVerdict(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		err      error
		want     string
	}{
		{
			name:     "raw 403 free tier refusal",
			provider: "opencode",
			err: &upstreamError{StatusCode: http.StatusForbidden,
				Body: []byte(`{"error":{"message":"free tier not available","type":"FreeTierError","code":403}}`)},
			want: "shape-rejected",
		},
		{
			name:     "raw 429 usage limit refusal",
			provider: "opencode",
			err: &upstreamError{StatusCode: http.StatusTooManyRequests,
				Body: []byte(`{"error":{"message":"Free usage limit reached. Subscribe to OpenCode Go to keep going","type":"FreeUsageLimitError","code":429}}`)},
			want: "ip-throttled",
		},
		{
			name:     "classified region block",
			provider: "opencode",
			err:      classifiedRefusal(t, http.StatusForbidden, `{"error":{"message":"not available in your country","type":"FreeTierError","code":403}}`),
			want:     "shape-rejected",
		},
		{
			name:     "classified quota exhaustion",
			provider: "opencode-go",
			err:      classifiedRefusal(t, http.StatusTooManyRequests, `{"error":{"message":"Free usage limit reached","type":"FreeUsageLimitError","code":429}}`),
			want:     "ip-throttled",
		},
		{
			name:     "classified abuse detection",
			provider: "opencode-zen",
			err:      classifiedRefusal(t, http.StatusForbidden, `{"error":{"message":"detect abuse traffic","type":"FreeTierError","code":403}}`),
			want:     "ip-abuse",
		},
		{
			name:     "classified generic unavailability",
			provider: "opencode",
			err:      classifiedRefusal(t, http.StatusForbidden, `{"error":{"message":"temporarily unavailable","type":"FreeTierError","code":403}}`),
			want:     "shape-rejected",
		},
		{
			name:     "classified version floor publishes nothing",
			provider: "opencode",
			err:      classifiedRefusal(t, http.StatusForbidden, `{"error":{"message":"upgrade required","type":"FreeTierError","code":403}}`),
			want:     "",
		},
		{
			name:     "wrapped classified refusal",
			provider: "opencode",
			err: fmt.Errorf("forward: %w", classifiedRefusal(t, http.StatusForbidden,
				`{"error":{"message":"not available in your country","type":"FreeTierError","code":403}}`)),
			want: "shape-rejected",
		},
		{
			name:     "another provider's refusal publishes nothing",
			provider: "gemini",
			err: &upstreamError{StatusCode: http.StatusForbidden,
				Body: []byte(`{"error":{"message":"free tier not available","type":"FreeTierError","code":403}}`)},
			want: "",
		},
		{
			name:     "genuine rate limit publishes nothing",
			provider: "opencode",
			err:      genuineRateLimit429(),
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			publishOpenCodeGateVerdict(rec, tt.provider, tt.err)
			if got := rec.Header().Get(headerXOpenCodeGate); got != tt.want {
				t.Fatalf("response header %s = %q, want %q", headerXOpenCodeGate, got, tt.want)
			}
			var ue *upstreamError
			if errors.As(tt.err, &ue) {
				if got := ue.Header.Get(headerXOpenCodeGate); got != tt.want {
					t.Errorf("error header %s = %q, want %q", headerXOpenCodeGate, got, tt.want)
				}
			}
		})
	}
}

func TestOpencodeEgressRotateMax(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want int
	}{
		{name: "default", env: "", want: opencodeEgressRotateMaxDefault},
		{name: "override", env: "2", want: 2},
		{name: "clamped", env: "99", want: 16},
		{name: "malformed falls back", env: "nope", want: opencodeEgressRotateMaxDefault},
		{name: "zero falls back", env: "0", want: opencodeEgressRotateMaxDefault},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("EGRESS_ROTATE_MAX", tt.env)
			if got := opencodeEgressRotateMax(); got != tt.want {
				t.Errorf("opencodeEgressRotateMax() = %d, want %d", got, tt.want)
			}
		})
	}
}
