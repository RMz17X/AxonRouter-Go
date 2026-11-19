package chat

import (
	"context"
	"errors"

	"axonrouter/proxy/internal/speedlayer"
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
// The count lives in the speed layer, not in this process: with more
// than one replica a local counter would admit one generation per node
// per account, which is exactly the overlap the upstream rejects.
// Boot still installs a memory store in single-node mode, so the
// single-replica behaviour is unchanged. A speed-layer error fails
// open — an unavailable cache must not take the gateway down, even
// at the cost of a missed cap on that attempt.
//
// The cap is 1 — strictly sequential per account, the shape a single
// interactive Antigravity user produces (and the shape that works when
// the account is driven from the official client). A cap of 3 shipped
// first and the post-flip census still read 89 failures against 3
// successes in 14 minutes: production generations are large streaming
// requests that occupy the account for tens of seconds, so any overlap
// at all draws the bare 429. The successes in that window clustered in
// the low-concurrency minutes right after the provider breaker closed,
// when attempts happened to run one at a time. Rotation across the
// account pool provides the fleet's parallelism instead.
const (
	antigravityMaxInFlightPerConn = 1
	connSlotKeyPrefix             = "connslot:"
)

var errConnInFlightCap = errors.New("connection at its in-flight cap")

func connSlotKey(connID string) string { return connSlotKeyPrefix + connID }

// tryAcquireConnSlot atomically takes one in-flight slot for the
// connection and reports whether it got one. An increment above the
// cap is rolled back immediately; the caller then rotates to another
// account without sending anything upstream.
func tryAcquireConnSlot(ctx context.Context, connID string, limit int) bool {
	if connID == "" || limit <= 0 {
		return true
	}
	store := speedlayer.Default()
	opCtx, cancel := speedlayer.OpContext(ctx)
	defer cancel()
	value, err := store.AddCounter(opCtx, connSlotKey(connID), 1)
	if err != nil {
		return true
	}
	if value <= int64(limit) {
		return true
	}
	// Release must not inherit the request's cancellation: the client
	// may be gone while the failure path still unwinds.
	rollbackCtx, rollbackCancel := speedlayer.OpContext(context.Background())
	defer rollbackCancel()
	_, _ = store.AddCounter(rollbackCtx, connSlotKey(connID), -1)
	return false
}

// releaseConnSlot returns one in-flight slot to the shared counter.
func releaseConnSlot(connID string) {
	if connID == "" {
		return
	}
	store := speedlayer.Default()
	ctx, cancel := speedlayer.OpContext(context.Background())
	defer cancel()
	value, err := store.AddCounter(ctx, connSlotKey(connID), -1)
	if err == nil && value < 0 {
		// A generation that outlived the counter's sliding TTL would
		// otherwise leave a negative slot behind and admit extra work.
		_ = store.Delete(ctx, connSlotKey(connID))
	}
}

// isConnInFlightCapError reports whether an attempt was refused by the
// in-flight cap (vs refused by the upstream).
func isConnInFlightCapError(err error) bool {
	return errors.Is(err, errConnInFlightCap)
}
