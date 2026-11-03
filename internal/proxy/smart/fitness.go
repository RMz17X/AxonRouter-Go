package smart

import (
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/db"
)

var (
	ErrNoPoolsSupplied = errors.New("no candidate proxy pools supplied")
)

// PoolStats holds in-memory health and fitness telemetry for a pool.
type PoolStats struct {
	PoolID              string        `json:"poolId"`
	Provider            string        `json:"provider"`
	SuccessCount        uint64        `json:"successCount"`
	FailureCount        uint64        `json:"failureCount"`
	ConsecutiveFails    uint64        `json:"consecutiveFails"`
	ConsecutiveProbeBad uint64        `json:"consecutiveProbeFails"`
	EWMALatencyMs       float64       `json:"ewmaLatencyMs"`
	LastSuccessAt       time.Time     `json:"lastSuccessAt"`
	LastFailureAt       time.Time     `json:"lastFailureAt"`
	CooldownUntil       time.Time     `json:"cooldownUntil"`
	FitnessScore        float64       `json:"fitnessScore"`
}

// FitnessTracker tracks per-provider, per-pool health and latency metrics.
type FitnessTracker struct {
	mu         sync.RWMutex
	stats      map[string]*PoolStats // key: provider + "\x00" + poolID
	probeFails map[string]uint64     // key: poolID -> consecutive probe failures
}

// DefaultTracker is the singleton instance used across the proxy lifecycle.
var DefaultTracker = NewFitnessTracker()

// NewFitnessTracker creates a new isolated FitnessTracker.
func NewFitnessTracker() *FitnessTracker {
	return &FitnessTracker{
		stats:      make(map[string]*PoolStats),
		probeFails: make(map[string]uint64),
	}
}

func statKey(provider, poolID string) string {
	return provider + "\x00" + poolID
}

// RecordSuccess records a successful live request for this pool on this provider.
func (t *FitnessTracker) RecordSuccess(provider, poolID string, latency time.Duration) {
	if poolID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	key := statKey(provider, poolID)
	s, ok := t.stats[key]
	if !ok {
		s = &PoolStats{
			PoolID:   poolID,
			Provider: provider,
		}
		t.stats[key] = s
	}

	s.SuccessCount++
	s.ConsecutiveFails = 0
	s.LastSuccessAt = time.Now()
	s.CooldownUntil = time.Time{}

	latMs := float64(latency.Milliseconds())
	if s.EWMALatencyMs <= 0 {
		s.EWMALatencyMs = latMs
	} else {
		// EWMA with alpha = 0.2
		s.EWMALatencyMs = 0.8*s.EWMALatencyMs + 0.2*latMs
	}
	s.FitnessScore = calculateScore(s, time.Now())
}

// RecordFailure records a live request failure for this pool on this provider.
func (t *FitnessTracker) RecordFailure(provider, poolID string, _ error) {
	if poolID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	key := statKey(provider, poolID)
	s, ok := t.stats[key]
	if !ok {
		s = &PoolStats{
			PoolID:   poolID,
			Provider: provider,
		}
		t.stats[key] = s
	}

	now := time.Now()
	s.FailureCount++
	s.ConsecutiveFails++
	s.LastFailureAt = now

	// Exponential cooldown progression: 1st=10s, 2nd=30s, 3+=60s
	var cooldownSec int
	switch s.ConsecutiveFails {
	case 1:
		cooldownSec = 10
	case 2:
		cooldownSec = 30
	default:
		cooldownSec = 60
	}
	s.CooldownUntil = now.Add(time.Duration(cooldownSec) * time.Second)
	s.FitnessScore = calculateScore(s, now)
}

// RecordProbe records a canary background probe result for this pool.
// Returns the updated consecutive probe failure count.
func (t *FitnessTracker) RecordProbe(poolID string, success bool, latency time.Duration) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()

	if success {
		t.probeFails[poolID] = 0
		return 0
	}
	t.probeFails[poolID]++
	return t.probeFails[poolID]
}

// GetProbeFailures returns consecutive probe failures for a pool.
func (t *FitnessTracker) GetProbeFailures(poolID string) uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.probeFails[poolID]
}

// Score computes the current fitness score (0.0 to 100.0) for a provider + pool.
func (t *FitnessTracker) Score(provider, poolID string) float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	key := statKey(provider, poolID)
	s, ok := t.stats[key]
	if !ok {
		return 100.0 // fresh pools start with full fitness
	}
	return calculateScore(s, time.Now())
}

func calculateScore(s *PoolStats, now time.Time) float64 {
	if s == nil {
		return 100.0
	}
	total := s.SuccessCount + s.FailureCount
	if total == 0 {
		return 100.0
	}

	// If currently in cooldown, heavy temporary penalty
	if now.Before(s.CooldownUntil) {
		return 5.0
	}

	successRatio := float64(s.SuccessCount) / float64(total)
	var latScore float64
	if s.EWMALatencyMs > 0 {
		latScore = (1000.0 / (1000.0 + s.EWMALatencyMs)) * 100.0
	} else {
		latScore = 100.0
	}

	base := (0.7 * (successRatio * 100.0)) + (0.3 * latScore)
	penalty := float64(s.ConsecutiveFails) * 20.0
	score := base - penalty

	if score < 10.0 {
		score = 10.0
	} else if score > 100.0 {
		score = 100.0
	}
	return math.Round(score*10) / 10
}

// PickBest selects the pool with the highest fitness score among candidates.
// Pools currently in cooldown are excluded unless all candidates are failing.
func (t *FitnessTracker) PickBest(provider string, candidates []*db.ProxyPool) (*db.ProxyPool, error) {
	if len(candidates) == 0 {
		return nil, ErrNoPoolsSupplied
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}

	now := time.Now()
	t.mu.RLock()
	defer t.mu.RUnlock()

	var bestHealthy *db.ProxyPool
	var bestScore float64 = -1

	var bestCooldownPool *db.ProxyPool
	var earliestExpiry time.Time

	for _, pool := range candidates {
		if pool == nil {
			continue
		}
		key := statKey(provider, pool.ID)
		s := t.stats[key]

		if s != nil && now.Before(s.CooldownUntil) {
			// In cooldown: track earliest expiring pool as fallback
			if bestCooldownPool == nil || s.CooldownUntil.Before(earliestExpiry) {
				bestCooldownPool = pool
				earliestExpiry = s.CooldownUntil
			}
			continue
		}

		score := calculateScore(s, now)
		if score > bestScore {
			bestScore = score
			bestHealthy = pool
		}
	}

	if bestHealthy != nil {
		return bestHealthy, nil
	}
	// All candidates on cooldown: fallback to the pool recovering soonest
	if bestCooldownPool != nil {
		return bestCooldownPool, nil
	}
	return candidates[0], nil
}

// PoolHealthSummary summarizes fitness & cooldown status for a pool across providers.
type PoolHealthSummary struct {
	PoolID                string  `json:"poolId"`
	InCooldown            bool    `json:"inCooldown"`
	CooldownRemainingSec  int     `json:"cooldownRemainingSec"`
	FitnessScore          float64 `json:"fitnessScore"`
	ConsecutiveFails      uint64  `json:"consecutiveFails"`
	ConsecutiveProbeFails uint64  `json:"consecutiveProbeFails"`
	EWMALatencyMs         float64 `json:"ewmaLatencyMs"`
}

// GetPoolHealthSummary returns the aggregated health, fitness and cooldown info for a pool.
func (t *FitnessTracker) GetPoolHealthSummary(poolID string) PoolHealthSummary {
	t.mu.RLock()
	defer t.mu.RUnlock()

	now := time.Now()
	summary := PoolHealthSummary{
		PoolID:                poolID,
		FitnessScore:          100.0,
		ConsecutiveProbeFails: t.probeFails[poolID],
	}

	found := false
	var totalScore float64
	var count float64
	var maxCooldown time.Time
	var maxFails uint64
	var avgLatency float64

	for key, s := range t.stats {
		parts := strings.Split(key, "\x00")
		if len(parts) == 2 && parts[1] == poolID {
			found = true
			score := calculateScore(s, now)
			totalScore += score
			count++
			if s.CooldownUntil.After(maxCooldown) {
				maxCooldown = s.CooldownUntil
			}
			if s.ConsecutiveFails > maxFails {
				maxFails = s.ConsecutiveFails
			}
			avgLatency += s.EWMALatencyMs
		}
	}

	if found && count > 0 {
		summary.FitnessScore = totalScore / count
		summary.ConsecutiveFails = maxFails
		summary.EWMALatencyMs = avgLatency / count
		if maxCooldown.After(now) {
			summary.InCooldown = true
			summary.CooldownRemainingSec = int(time.Until(maxCooldown).Seconds())
		}
	}

	return summary
}

// Snapshot returns a copy of all current pool stats.
func (t *FitnessTracker) Snapshot() []PoolStats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	now := time.Now()
	res := make([]PoolStats, 0, len(t.stats))
	for _, s := range t.stats {
		cp := *s
		cp.FitnessScore = calculateScore(s, now)
		res = append(res, cp)
	}
	return res
}
