package speedlayer

import (
	"context"
	"database/sql"
	"os"
	"strings"

	"9router/proxy/internal/log"
)

// Mode names the boot selection, for the banner and the status payload.
type Mode string

const (
	// ModeSingle: no Redis configured — in-memory store, one node.
	ModeSingle Mode = "single"
	// ModeCluster: Redis configured — Redis primary, database
	// degradation armed behind the breaker.
	ModeCluster Mode = "cluster"
)

// Status is the speed layer's report of itself: what boot selected,
// what is serving right now, and whether the breaker has degraded the
// cluster onto the database backend. The dashboard's system payload
// embeds it verbatim.
type Status struct {
	Mode     Mode   `json:"mode"`
	NodeID   string `json:"nodeId"`
	Primary  string `json:"primary"`
	Serving  string `json:"serving"`
	Degraded bool   `json:"degraded"`
}

var bootStatus = Status{Mode: ModeSingle, Primary: "memory", Serving: "memory"}

// CurrentStatus returns the status recorded at boot, with the serving
// backend refreshed from the live breaker when one exists.
func CurrentStatus() Status {
	s := bootStatus
	if f, ok := Default().(*failoverStore); ok {
		s.Degraded = f.Degraded()
		s.Serving = f.Name()
	}
	return s
}

// NodeID identifies this process in logs and status payloads: NODE_ID
// when the operator sets it (compose service names are the natural
// value), else the hostname.
func NodeID() string {
	if id := strings.TrimSpace(os.Getenv("NODE_ID")); id != "" {
		return id
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	return "unknown-node"
}

// RedisURLFromEnv returns the configured Redis/Valkey URL: VALKEY_URL
// first (Valkey is the chosen flavour, decision D4), then REDIS_URL.
func RedisURLFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("VALKEY_URL")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("REDIS_URL"))
}

// Build selects and constructs the store for this boot and records the
// boot status. handle/dialect describe the application database (the
// degradation backend lives in it). Selection:
//
//   - REDIS_URL/VALKEY_URL set → failover(redis, database); an eager
//     Ping decides whether the breaker starts open. A URL that does not
//     parse is an operator error: log it loudly and run cluster-shaped
//     anyway (database serving, breaker open) rather than silently
//     falling back to per-node memory, which the design forbids.
//   - unset → memory (single-node mode).
func Build(handle *sql.DB, dialect string) Store {
	node := NodeID()
	raw := RedisURLFromEnv()
	if raw == "" {
		bootStatus = Status{Mode: ModeSingle, NodeID: node, Primary: "memory", Serving: "memory"}
		return NewMemoryStore()
	}

	dbStore, err := NewDBStore(handle, dialect)
	if err != nil {
		// The database is the application's own handle, already proven
		// by boot reaching this point; landing here means the speed
		// tables could not be created. Degrading to memory is the last
		// resort and is logged as such — it can only happen while the
		// database itself is broken, in which case the gateway is
		// already not serving.
		log.Error("speedlayer", "database degradation backend unavailable; speed layer has no shared store", "error", err)
		bootStatus = Status{Mode: ModeCluster, NodeID: node, Primary: "redis", Serving: "memory", Degraded: true}
		return NewMemoryStore()
	}

	redisStore, err := NewRedisStore(raw)
	if err != nil {
		log.Error("speedlayer", "redis URL unusable; serving the speed layer from the database backend", "error", err)
		bootStatus = Status{Mode: ModeCluster, NodeID: node, Primary: "redis", Serving: "database", Degraded: true}
		return dbStore
	}

	f := newFailoverStore(redisStore, dbStore)
	bootStatus = Status{Mode: ModeCluster, NodeID: node, Primary: "redis", Serving: "redis"}
	ctx, cancel := context.WithTimeout(context.Background(), respDialTO+respCommandTO)
	pingErr := redisStore.Ping(ctx)
	cancel()
	if pingErr != nil {
		f.startDegraded()
		bootStatus.Serving = "database"
		bootStatus.Degraded = true
	}
	return f
}

// Banner returns the one-line boot banner values: mode, node identity,
// and the backends in use (database dialect + speed layer). The app
// logs it once at startup.
func Banner(dbDialect string) (mode string, node string, backends string) {
	s := CurrentStatus()
	return string(s.Mode), s.NodeID, "db=" + dbDialect + " speed=" + s.Serving + " primary=" + s.Primary
}
