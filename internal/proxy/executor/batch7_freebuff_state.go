package executor

// FreebuffSessionStateSize reports the freebuff executor's in-process state
// sizes in the shape GET /api/system/memory returns (batch 7, AxonRouter
// parity with open-sse/executors/freebuff.js sessionStateSize()).
//
// Only "sessions" is an in-process map in the Go executor. The other four
// counters upstream reads from JS Maps — inflight turns, model locks, pool
// limits, offer caches — live in the lease table and the resilience layer
// here, not in process memory, so they report 0 rather than a fabricated
// in-memory size.
func FreebuffSessionStateSize() map[string]int {
	freebuffSessionMu.RLock()
	sessions := len(freebuffSessionCache)
	freebuffSessionMu.RUnlock()
	return map[string]int{
		"sessions":    sessions,
		"inflight":    0,
		"modelLocks":  0,
		"poolLimits":  0,
		"offerCaches": 0,
	}
}
