package chat

import (
	"errors"
	"sync"
)

// conn_inflight.go — per-connection in-flight caps for providers whose
// upstream enforces a per-account concurrency ceiling.
//
// Antigravity's consumer endpoint allows only a few concurrent
// generations per account: past the ceiling it answers the bare
// RESOURCE_EXHAUSTED 429 (no quota details, no retry hint), which the
// serving path historically recorded as quota exhaustion. A single
// interactive user never reaches the ceiling — but the gateway does,
// by hedging, retrying, and funnelling several clients onto the same
// healthy accounts. Capping the attempts in flight per connection
// keeps the gateway under the ceiling; an attempt that finds the cap
// full fails fast with errConnInFlightCap and the caller rotates to
// the next account instead of manufacturing another upstream 429.
//
// The registry is in-memory only: a restart forgets the counts, which
// is correct — in-flight attempts do not survive one either.
const antigravityMaxInFlightPerConn = 3

var errConnInFlightCap = errors.New("connection at its in-flight cap")

var (
	connInFlightMu sync.Mutex
	connInFlight   = make(map[string]int)
)

// tryAcquireConnSlot takes one in-flight slot for the connection and
// reports whether it got one.
func tryAcquireConnSlot(connID string, limit int) bool {
	if connID == "" || limit <= 0 {
		return true
	}
	connInFlightMu.Lock()
	defer connInFlightMu.Unlock()
	if connInFlight[connID] >= limit {
		return false
	}
	connInFlight[connID]++
	return true
}

// releaseConnSlot returns one in-flight slot for the connection.
func releaseConnSlot(connID string) {
	if connID == "" {
		return
	}
	connInFlightMu.Lock()
	defer connInFlightMu.Unlock()
	if connInFlight[connID] <= 1 {
		delete(connInFlight, connID)
		return
	}
	connInFlight[connID]--
}

// isConnInFlightCapError reports whether an attempt was refused by the
// in-flight cap (vs refused by the upstream).
func isConnInFlightCapError(err error) bool {
	return errors.Is(err, errConnInFlightCap)
}
