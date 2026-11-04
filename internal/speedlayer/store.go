// Package speedlayer is the cluster speed layer (P3 of the PostgreSQL /
// cluster program): the home for ephemeral, hot-path state that more than
// one gateway node must agree on — in-flight counters, the recent-request
// ring, rotation cursors, and short-lived key/value entries such as
// per-model cooldown locks.
//
// Everything held here is a cache of something the database already knows
// or a value that expires on its own. The database (PostgreSQL in cluster
// mode, the SQLite file in single-node mode) is always the durable truth;
// every entry in this layer must be reconstructible from it or safe to
// lose. Nothing here may become the only copy of anything.
//
// Three backends implement Store:
//
//   - memory: today's in-process state, extracted. Single-node mode.
//   - redis: a Valkey/Redis server (REDIS_URL or VALKEY_URL), the cluster
//     primary — a minimal hand-rolled RESP client, no new dependency.
//   - database: tables inside the application database itself. This is
//     the degradation backend: when Redis is configured but unreachable,
//     the failover store routes here — NEVER to per-node memory, which
//     would let cluster nodes diverge.
//
// Boot selection (see config.go): Redis configured → failover(redis,
// database) with a health-checked breaker; no Redis → memory.
package speedlayer

import (
	"context"
	"sync/atomic"
	"time"
)

// Store is the speed layer's contract. Implementations must be safe for
// concurrent use. A nil error with an absent value is reported through
// the `ok` result, never as an error: an unreachable backend IS an error,
// an expired key is not.
type Store interface {
	// Name identifies the backend for the boot banner and the dashboard
	// status payload: "memory", "redis", or "database".
	Name() string

	// Ping checks backend reachability (the failover breaker probes the
	// primary with it).
	Ping(ctx context.Context) error

	// AddCounter atomically adds delta to the counter at key and returns
	// the new value. Counters carry a sliding expiry (counterTTL,
	// refreshed on every write) so a node that dies mid-request cannot
	// leak a phantom count forever.
	AddCounter(ctx context.Context, key string, delta int64) (int64, error)

	// Counters returns every live counter whose key starts with prefix.
	Counters(ctx context.Context, prefix string) (map[string]int64, error)

	// Set stores value at key with a TTL. A zero TTL means no expiry;
	// callers should always pass one — unbounded keys are how a cache
	// becomes a leak.
	Set(ctx context.Context, key, value string, ttl time.Duration) error

	// Get reads key. ok=false means absent or expired.
	Get(ctx context.Context, key string) (value string, ok bool, err error)

	// GetMany reads several keys in one round trip; absent keys are
	// simply missing from the result.
	GetMany(ctx context.Context, keys []string) (map[string]string, error)

	// Delete drops key (absent is not an error).
	Delete(ctx context.Context, key string) error

	// DeletePrefix drops every key under prefix and reports how many.
	DeletePrefix(ctx context.Context, prefix string) (int, error)

	// RingPush prepends value to the bounded list at key, trimming it to
	// cap entries (newest first).
	RingPush(ctx context.Context, key, value string, cap int) error

	// RingRead returns up to limit entries of the list at key, newest
	// first.
	RingRead(ctx context.Context, key string, limit int) ([]string, error)
}

// counterTTL is the sliding expiry every counter key carries. In-flight
// counts are refreshed by the finish event seconds later; ten minutes
// only ever fires for a node that died holding a count, which is exactly
// the leak it exists to bound.
const counterTTL = 10 * time.Minute

// opTimeout bounds one speed-layer call from the hot paths that use the
// package-level Default store. The layer is an optimisation and a
// sharing mechanism, never a reason to stall a request: a slow backend
// must surface as an error the caller can drop, fast. One second is the
// measured compromise (P3): a co-located Valkey answers in ~1 ms, but a
// tunnelled or cross-site one can cost ~300 ms per round trip (measured
// on the SSH-forward staging rig, where two-command ops reach ~620 ms),
// and a 300 ms budget timed every such op out and tripped the failover
// breaker against a perfectly healthy server. Past a second the breaker
// (three consecutive failures) moves traffic to the database anyway.
const opTimeout = time.Second

// OpContext derives the bounded context hot-path callers use for store
// operations.
func OpContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, opTimeout)
}

// defaultStore is the process-wide store boot installs. Until Install
// runs it is a memory store, which is exactly the pre-P3 behaviour —
// partial boots (tests, tooling) therefore keep working unchanged.
var defaultStore atomic.Pointer[storeHolder]

type storeHolder struct{ store Store }

func init() {
	defaultStore.Store(&storeHolder{store: NewMemoryStore()})
}

// Default returns the process-wide store. It is never nil.
func Default() Store {
	return defaultStore.Load().store
}

// Install replaces the process-wide store. Boot calls it exactly once,
// after the database handle exists (the database backend shares it).
func Install(s Store) {
	if s != nil {
		defaultStore.Store(&storeHolder{store: s})
	}
}
