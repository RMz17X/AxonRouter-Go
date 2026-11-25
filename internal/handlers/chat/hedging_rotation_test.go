package chat

import (
	"fmt"
	"testing"
	"time"

	"axonrouter/proxy/internal/models"
	"axonrouter/proxy/internal/proxy"
)

func TestRecordHedgeCandidateFailureTokenHarbor429NoRepo(t *testing.T) {
	h := &ChatHandler{}
	err := &proxy.UpstreamError{
		StatusCode: 429,
		Body:       []byte(`{"error":{"message":"Rate limit reached. You can send your next request in 59 min 1 s.","type":"rate_limit_exceeded"}}`),
	}
	h.recordHedgeCandidateFailure(nil, "tokenharbor", "qwen3.8-flash:free", "conn-1", err)
}

func TestTokenHarborHedgePolicy(t *testing.T) {
	h := &ChatHandler{}
	enabled, delay, maxAttempts := h.shouldHedgeProvider("tokenharbor")
	if !enabled || delay != 250*time.Millisecond || maxAttempts != 16 {
		t.Fatalf("hedge policy = (%v, %v, %d), want (true, 250ms, 16)", enabled, delay, maxAttempts)
	}
	if budget := h.hedgeAdmissionTimeout("tokenharbor"); budget != 45*time.Second {
		t.Fatalf("admission budget = %v, want 45s", budget)
	}
	if wt := tokenharborWaveTimeout("tokenharbor"); wt != 6*time.Second {
		t.Fatalf("wave budget = %v, want 6s", wt)
	}
	if wt := tokenharborWaveTimeout("mmg"); wt != 0 {
		t.Fatalf("non-tokenharbor wave budget = %v, want 0", wt)
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
