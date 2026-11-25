package chat

import (
	"net/http"
	"testing"
	"time"

	"axonrouter/proxy/internal/providers"
	"axonrouter/proxy/internal/proxy"
)

func TestTokenHarborRetryAfterHeaderWins(t *testing.T) {
	ue := &proxy.UpstreamError{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"37"}},
		Body:       []byte(`{"error":{"message":"Rate limit reached. You can send your next request in 59 min 1 s."}}`),
	}
	got := cooldownSecForClassification(http.StatusTooManyRequests, providers.ErrorClassification{CooldownMs: 2000}, ue, "tokenharbor")
	if got != 37 {
		t.Fatalf("cooldown = %d, want 37 from Retry-After header", got)
	}
}

func TestTokenHarborRollingAllowanceCooldown(t *testing.T) {
	ue := &proxy.UpstreamError{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"37"}},
		Body:       []byte(`{"error":{"message":"You've used this period's free allowance. Your next rolling 7-day period starts on 2026-12-01."}}`),
	}
	got := cooldownSecForClassification(http.StatusTooManyRequests, providers.ErrorClassification{}, ue, "tokenharbor")
	if got != 7*24*60*60 {
		t.Fatalf("rolling cooldown = %d, want %d", got, 7*24*60*60)
	}
}

func TestProviderBreakerTokenHarbor429DoesNotCount(t *testing.T) {
	err := &proxy.UpstreamError{StatusCode: http.StatusTooManyRequests, Body: []byte(`{"error":{"message":"Rate limit reached. You can send your next request in 59 min 1 s."}}`)}
	if providerBreakerShouldCount("tokenharbor", err) {
		t.Fatal("tokenharbor account 429 must not open model-wide breaker")
	}
	serverErr := &proxy.UpstreamError{StatusCode: http.StatusInternalServerError, Body: []byte(`{"error":"boom"}`)}
	if !providerBreakerShouldCount("tokenharbor", serverErr) {
		t.Fatal("tokenharbor 5xx must still count toward the breaker")
	}
}

func TestProviderBreakerHalfOpenProbe(t *testing.T) {
	ResetProviderBreaker()
	defer ResetProviderBreaker()

	const provider = "tokenharbor"
	const model = "mimo-v2.6-flash:free"

	for i := 0; i < providerBreakerFailureThreshold; i++ {
		RecordProviderModelFailure(provider, model)
	}
	if !IsProviderModelBroken(provider, model) {
		t.Fatal("circuit should be open after threshold failures")
	}

	key := providerBreakerKey(provider, model)
	providerBreakerMu.Lock()
	providerBreaker[key].blockedUntil = time.Now().Add(-time.Second)
	providerBreakerMu.Unlock()

	if IsProviderModelBroken(provider, model) {
		t.Fatal("first caller after cooldown should claim the half-open probe")
	}
	if !IsProviderModelBroken(provider, model) {
		t.Fatal("second caller must wait while the probe is in flight")
	}

	RecordProviderModelSuccess(provider, model)
	if IsProviderModelBroken(provider, model) {
		t.Fatal("a successful probe should close the circuit")
	}
}

func TestProviderBreakerFailedProbeExtendsCooldown(t *testing.T) {
	ResetProviderBreaker()
	defer ResetProviderBreaker()

	const provider = "tokenharbor"
	const model = "qwen3.8-flash:free"

	for i := 0; i < providerBreakerFailureThreshold; i++ {
		RecordProviderModelFailure(provider, model)
	}
	key := providerBreakerKey(provider, model)
	providerBreakerMu.Lock()
	providerBreaker[key].blockedUntil = time.Now().Add(-time.Second)
	providerBreakerMu.Unlock()

	if IsProviderModelBroken(provider, model) {
		t.Fatal("probe should have been claimed")
	}
	RecordProviderModelFailure(provider, model)
	if !IsProviderModelBroken(provider, model) {
		t.Fatal("failed probe should keep circuit open")
	}
}
