package smart_test

import (
	"errors"
	"testing"
	"time"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/proxy/smart"
)

func TestFitnessTracker_ScoreProgression(t *testing.T) {
	tracker := smart.NewFitnessTracker()
	provider := "anthropic"
	poolID := "pool-1"

	// Fresh pool has full score 100
	if score := tracker.Score(provider, poolID); score != 100.0 {
		t.Fatalf("expected fresh pool score 100.0, got %f", score)
	}

	// 1 failure puts pool on cooldown (score drops to 5.0)
	tracker.RecordFailure(provider, poolID, errors.New("timeout"))
	if score := tracker.Score(provider, poolID); score != 5.0 {
		t.Fatalf("expected cooldown score 5.0, got %f", score)
	}

	// Success restores score and clears cooldown
	tracker.RecordSuccess(provider, poolID, 150*time.Millisecond)
	score := tracker.Score(provider, poolID)
	if score <= 5.0 {
		t.Fatalf("expected score > 5.0 after success, got %f", score)
	}
}

func TestFitnessTracker_PickBest(t *testing.T) {
	tracker := smart.NewFitnessTracker()
	provider := "openai"

	p1 := &db.ProxyPool{ID: "pool-fast", Name: "Fast Pool", IsActive: true}
	p2 := &db.ProxyPool{ID: "pool-slow", Name: "Slow Pool", IsActive: true}
	p3 := &db.ProxyPool{ID: "pool-failing", Name: "Failing Pool", IsActive: true}

	// p1 has 10 successes at 50ms
	for i := 0; i < 10; i++ {
		tracker.RecordSuccess(provider, p1.ID, 50*time.Millisecond)
	}

	// p2 has 5 successes at 800ms
	for i := 0; i < 5; i++ {
		tracker.RecordSuccess(provider, p2.ID, 800*time.Millisecond)
	}

	// p3 has failure (on cooldown)
	tracker.RecordFailure(provider, p3.ID, errors.New("conn refused"))

	candidates := []*db.ProxyPool{p3, p2, p1}
	best, err := tracker.PickBest(provider, candidates)
	if err != nil {
		t.Fatalf("PickBest returned error: %v", err)
	}
	if best.ID != p1.ID {
		t.Fatalf("expected p1 (fast) to be picked, got %s", best.ID)
	}

	// Now fail p1 so it goes on cooldown
	tracker.RecordFailure(provider, p1.ID, errors.New("502 bad gateway"))

	// Best should now be p2
	best2, err := tracker.PickBest(provider, candidates)
	if err != nil {
		t.Fatalf("PickBest returned error: %v", err)
	}
	if best2.ID != p2.ID {
		t.Fatalf("expected p2 (slow but healthy) to be picked, got %s", best2.ID)
	}

	// Now fail p2 as well: all candidates on cooldown!
	tracker.RecordFailure(provider, p2.ID, errors.New("timeout"))

	// All on cooldown -> soft failover should pick one rather than erroring out
	bestAllFailing, err := tracker.PickBest(provider, candidates)
	if err != nil {
		t.Fatalf("PickBest all failing should soft failover, got error: %v", err)
	}
	if bestAllFailing == nil {
		t.Fatalf("expected a fallback candidate, got nil")
	}
}

func TestFitnessTracker_PerProviderIsolation(t *testing.T) {
	tracker := smart.NewFitnessTracker()
	poolID := "shared-pool"

	// Pool fails on Anthropic
	tracker.RecordFailure("anthropic", poolID, errors.New("blocked"))
	if s := tracker.Score("anthropic", poolID); s != 5.0 {
		t.Errorf("anthropic score should be 5.0 (cooldown), got %f", s)
	}

	// Same pool succeeds on OpenAI -> OpenAI score should be high!
	tracker.RecordSuccess("openai", poolID, 100*time.Millisecond)
	if s := tracker.Score("openai", poolID); s < 80.0 {
		t.Errorf("openai score should be high (>80), got %f", s)
	}
}
