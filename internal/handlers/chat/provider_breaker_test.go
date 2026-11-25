package chat

import (
	"testing"
	"time"
)

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
