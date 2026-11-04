// Package poolstate holds the engine-layer pool fitness marks and egress
// geo cache — the Go counterpart of AxonRouter's open-sse/services
// proxyPoolFitness.js + poolGeo.js. State is process-wide (a single
// registry), persisted to the settings snapshot under the same key
// upstream uses (proxyPoolFitness) so a database shared with the upstream
// dashboard keeps one view of which pools are parked for which scope.
package poolstate

import (
	"strings"
	"sync"
	"time"
)

// Mark is one pool's unfit verdict for one scope ("provider::model").
type Mark struct {
	Until  int64  `json:"until"` // epoch milliseconds
	Reason string `json:"reason"`
}

// Snapshot is poolId -> scope -> Mark, the persisted + API-facing shape.
type Snapshot map[string]map[string]Mark

// Persister is the storage seam for the fitness snapshot. The dashboard
// wires it to the settings row; the engine never imports the db package.
type Persister interface {
	LoadPoolFitness() (Snapshot, error)
	SavePoolFitness(Snapshot) error
}

const hydrateTTL = 60 * time.Second

var (
	mu          sync.Mutex
	marks       = map[string]map[string]Mark{}
	persister   Persister
	lastHydrate time.Time
	hydrated    bool
)

// EnsureHydrated loads the persisted snapshot when the registry has never
// loaded or the last load is older than the TTL (upstream re-hydrates on
// the same cadence so marks written by another process surface).
func EnsureHydrated(p Persister) {
	if p == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	persister = p
	if hydrated && time.Since(lastHydrate) < hydrateTTL {
		return
	}
	snap, err := p.LoadPoolFitness()
	if err != nil {
		return
	}
	now := time.Now().UnixMilli()
	for poolID, byScope := range snap {
		live := map[string]Mark{}
		for scope, m := range byScope {
			if m.Until > now {
				live[scope] = m
			}
		}
		if len(live) > 0 {
			marks[poolID] = live
		}
	}
	hydrated = true
	lastHydrate = time.Now()
}

// MarkUnfit parks a pool for a scope until the given epoch-ms deadline.
func MarkUnfit(poolID, scope string, until int64, reason string) {
	if poolID == "" || scope == "" {
		return
	}
	mu.Lock()
	byScope, ok := marks[poolID]
	if !ok {
		byScope = map[string]Mark{}
		marks[poolID] = byScope
	}
	byScope[scope] = Mark{Until: until, Reason: reason}
	mu.Unlock()
	persist()
}

// SnapshotLive returns the non-expired marks, pruning expired entries.
func SnapshotLive() Snapshot {
	mu.Lock()
	defer mu.Unlock()
	now := time.Now().UnixMilli()
	out := Snapshot{}
	for poolID, byScope := range marks {
		for scope, m := range byScope {
			if m.Until <= now {
				delete(byScope, scope)
			}
		}
		if len(byScope) == 0 {
			delete(marks, poolID)
			continue
		}
		cp := make(map[string]Mark, len(byScope))
		for scope, m := range byScope {
			cp[scope] = m
		}
		out[poolID] = cp
	}
	return out
}

// Clear removes one pool+scope mark. A scope ending in "::*" clears every
// scope of that provider for the pool (upstream clearPoolUnfit semantics).
func Clear(poolID, scope string) {
	mu.Lock()
	if byScope, ok := marks[poolID]; ok {
		if strings.HasSuffix(scope, "::*") {
			prefix := scope[:len(scope)-1]
			for s := range byScope {
				if strings.HasPrefix(s, prefix) || s == scope {
					delete(byScope, s)
				}
			}
		} else {
			delete(byScope, scope)
		}
		if len(byScope) == 0 {
			delete(marks, poolID)
		}
	}
	mu.Unlock()
	persist()
}

// ClearAll removes every mark, or only the scopes of one provider when
// provider is non-empty (upstream clearAllPoolUnfit).
func ClearAll(provider string) {
	mu.Lock()
	if provider == "" {
		marks = map[string]map[string]Mark{}
	} else {
		prefix := provider + "::"
		for poolID, byScope := range marks {
			for s := range byScope {
				if strings.HasPrefix(s, prefix) {
					delete(byScope, s)
				}
			}
			if len(byScope) == 0 {
				delete(marks, poolID)
			}
		}
	}
	mu.Unlock()
	persist()
}

// persist writes the live snapshot through the wired persister. Fitness is
// advisory: a failed write must never block a request, so errors drop.
func persist() {
	mu.Lock()
	p := persister
	mu.Unlock()
	if p == nil {
		return
	}
	_ = p.SavePoolFitness(SnapshotLive())
}
