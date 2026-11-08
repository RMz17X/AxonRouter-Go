package chat

import (
	"testing"
	"time"
)

func TestDahlConcurrencyCache(t *testing.T) {
	ResetDahlModelConcurrency()
	defer ResetDahlModelConcurrency()

	model := "zai-org/GLM-5.3-Flash"

	if IsDahlModelConcurrencyBlocked(model) {
		t.Fatal("expected model not to be blocked initially")
	}
	if quotaCacheBlocked("dahl", "conn-1", model) {
		t.Fatal("expected quotaCacheBlocked to return false initially")
	}

	// Record block with 50ms cooldown
	RecordDahlModelConcurrencyBlocked(model, 50*time.Millisecond)

	if !IsDahlModelConcurrencyBlocked(model) {
		t.Fatal("expected model to be blocked after recording concurrency saturation")
	}
	if !quotaCacheBlocked("dahl", "conn-1", model) {
		t.Fatal("expected quotaCacheBlocked to return true while model is concurrency-blocked")
	}

	// Other models should NOT be blocked
	otherModel := "MiniMaxAI/MiniMax-M2.7"
	if IsDahlModelConcurrencyBlocked(otherModel) {
		t.Fatal("other model must NOT be blocked")
	}
	if quotaCacheBlocked("dahl", "conn-1", otherModel) {
		t.Fatal("other model must NOT be quotaCacheBlocked")
	}

	// Wait for expiration
	time.Sleep(60 * time.Millisecond)

	if IsDahlModelConcurrencyBlocked(model) {
		t.Fatal("expected model block to expire after cooldown duration")
	}
	if quotaCacheBlocked("dahl", "conn-1", model) {
		t.Fatal("expected quotaCacheBlocked to return false after expiration")
	}
}
