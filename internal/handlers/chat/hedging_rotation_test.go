package chat

import (
	"fmt"
	"testing"
	"time"

	"axonrouter/proxy/internal/models"
)

func TestTokenHarborHedgePolicy(t *testing.T) {
	h := &ChatHandler{}
	enabled, delay, maxAttempts := h.shouldHedgeProvider("tokenharbor")
	if !enabled || delay != time.Second || maxAttempts != 16 {
		t.Fatalf("hedge policy = (%v, %v, %d), want (true, 1s, 16)", enabled, delay, maxAttempts)
	}
	if budget := h.hedgeAdmissionTimeout("tokenharbor"); budget != 45*time.Second {
		t.Fatalf("admission budget = %v, want 45s", budget)
	}
	enabled, _, maxAttempts = h.shouldHedgeProvider("mmg")
	if enabled || maxAttempts != 2 {
		t.Fatalf("non-tokenharbor hedge policy = (%v, %d), want (false, 2)", enabled, maxAttempts)
	}
}

func TestPickHedgeCandidatesTokenHarborRotates(t *testing.T) {
	conns := make([]*models.ProviderConnection, 20)
	for i := range conns {
		conns[i] = &models.ProviderConnection{ID: fmt.Sprintf("conn-%02d", i), IsActive: 1}
	}
	tokenharborHedgeCursor.Store(0)

	h := &ChatHandler{}
	first := h.pickHedgeCandidates(conns, "tokenharbor", "mimo-v2.6-flash:free", 4)
	second := h.pickHedgeCandidates(conns, "tokenharbor", "mimo-v2.6-flash:free", 4)

	if len(first) != 4 || len(second) != 4 {
		t.Fatalf("candidate counts = %d/%d, want 4/4", len(first), len(second))
	}
	if first[0].ID == second[0].ID {
		t.Fatalf("tokenharbor hedge reused the same first account %q", first[0].ID)
	}

	// Non-tokenharbor providers keep the original stable order.
	stableA := h.pickHedgeCandidates(conns, "deepseek", "deepseek-chat", 4)
	stableB := h.pickHedgeCandidates(conns, "deepseek", "deepseek-chat", 4)
	for i := range stableA {
		if stableA[i].ID != stableB[i].ID {
			t.Fatalf("non-tokenharbor rotation changed candidate %d: %q vs %q", i, stableA[i].ID, stableB[i].ID)
		}
	}
}
