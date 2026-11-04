package db

import (
	"database/sql"
	"sync"
	"sync/atomic"
	"time"
)

// Repo wraps the database handle and groups all persistence queries.
//
// The queries live in files named after the table they touch, so this file
// holds only the type every reader hangs off:
//
//	apikeys.go        apiKeys
//	connections.go    providerConnections
//	providernodes.go  providerNodes
//	combos.go         combos
//	aliases.go        kv scopes: modelAliases, customModels
type Repo struct {
	db *sql.DB

	// activePoolIDs caches ActivePoolIDs. It lives on the Repo rather than in
	// package state because the answer belongs to one database: two Repos over
	// different handles would otherwise read each other's pool list.
	activePoolIDs atomic.Value // *[]string
	// jbRules caches the resolved jailbreak configuration (exact bindings,
	// presets, keyword and exclude rules) as one immutable snapshot, so a
	// request resolves injection without querying or compiling anything.
	// Every jailbreak write stores nil to invalidate it. Like activePoolIDs
	// it lives on the Repo because the answer belongs to one database.
	jbRules atomic.Value // *jailbreakRuleset
	// jbBuild serialises snapshot rebuilds so concurrent first requests after
	// a write build it once.
	jbBuild sync.Mutex
	// settingsCache holds the parsed settings row. It lives on the Repo for
	// the same reason activePoolIDs does: the answer belongs to one database.
	settingsCache atomic.Pointer[cachedSettings]
	// settingsRow holds the RAW settings row with its fetch time: the
	// short-TTL read cache behind GetSettings/GetSettingsRaw (review
	// F1). The parse cache above keys off this raw content, so the two
	// compose: the row is re-read at most once per TTL window, and only
	// re-parsed when it actually changed.
	settingsRow atomic.Pointer[cachedSettingsRow]
	// deprecationProviders is the in-memory set of providers that have
	// at least one recorded model deprecation (nil = not loaded). It
	// lets GetModelDeprecation answer "nothing recorded" for the
	// overwhelmingly common provider without a point query per request
	// (review F1). Loaded lazily from the kv scope, maintained by the
	// deprecation writers in deprecations.go.
	deprecationMu        sync.Mutex
	deprecationProviders map[string]bool
	// providerConns is the short-TTL snapshot behind
	// GetProviderConnections (review m1): selection used to re-read and
	// re-parse every connection row of a provider on every request.
	// Guarded by providerConnsMu; entries are deep clones, never the
	// slices handed to callers.
	providerConnsMu sync.Mutex
	providerConns   map[providerConnsKey]providerConnsEntry
	// vault is the optional credential vault. It is stored rather than held by
	// value so a key rotation can swap the live vault without touching any
	// reader that already captured a *Repo. A nil value means "no vault", which
	// is every install that never set ROUTER_MASTER_KEY.
	vault atomic.Value // *vault.Vault
	// speed is the optional cluster speed layer (P3), installed by the
	// app wiring via SetSpeedStore. Like the vault it is stored rather
	// than held by value so the wiring can install it after the Repo is
	// constructed. A nil value means "no layer" — every consumer in
	// speed.go then no-ops and the database reads carry the behaviour.
	speed atomic.Value // speedlayer.Store
	// budgetColsOnce/budgetCols cache whether the apiKeys table carries the
	// F-2 budget columns. The answer is a property of the schema, which does
	// not change while the process runs, and the probe sits on the dashboard's
	// boot path — so it runs once, like settingsCache.
	budgetColsOnce sync.Once
	budgetCols     bool
}

// cachedSettings is one parsed settings row and the row content it was
// parsed from.
type cachedSettings struct {
	raw   string
	value *SettingsData
}

// cachedSettingsRow is the raw settings row (id = 1) as last read or
// written through this Repo, with the moment it was captured. found is
// false when the row does not exist yet (fresh install).
type cachedSettingsRow struct {
	raw       string
	found     bool
	fetchedAt time.Time
}

// settingsRowTTL bounds how long the raw settings row may be served
// from memory. Writers through this Repo refresh the cache with the
// exact row they wrote (write-through, see settings.go), so the TTL
// only ever covers writers that bypass the Repo: a second process, the
// shared Next.js dashboard, the backup import. Five seconds matches
// the auth-cache precedent in middleware (a revoked key cannot outlive
// its 5 s window either).
const settingsRowTTL = 5 * time.Second

// nowUTC is the timestamp format every connection write stamps.
func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

// NewRepo creates a new repository instance using the provided SQL database connection.
func NewRepo(db *sql.DB) *Repo {
	return &Repo{db: db}
}

// RawDB returns the underlying *sql.DB connection for direct queries.
func (r *Repo) RawDB() *sql.DB {
	return r.db
}
