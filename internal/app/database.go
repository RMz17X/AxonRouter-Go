package app

import (
	"context"
	"database/sql"
	"fmt"

	"go.uber.org/fx"

	"axonrouter/proxy/internal/config"
	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/speedlayer"
	"axonrouter/proxy/internal/translator"
	"axonrouter/proxy/internal/usagetracker"
)

// DatabaseModule handles database initialization and provides *sql.DB and *db.Repo.
var DatabaseModule = fx.Module("database",
	fx.Provide(
		ProvideDatabase,
		ProvideRepo,
	),
	fx.Invoke(InitSpeedLayer),
)

// InitSpeedLayer builds the cluster speed layer (P3) over the booted
// database handle and installs it with every consumer: the process-wide
// default (the chat rotation cursors), the usage tracker (its live
// state), and the Repo (the model-lock overlay). Selection lives in
// speedlayer.Build: REDIS_URL/VALKEY_URL set → Redis primary with the
// database as the degradation backend behind a breaker; unset → the
// in-memory single-node store.
//
// The boot banner it logs is the operator's one-line answer to "what is
// this node": its identity, its mode (single/cluster), and the backends
// in use.
func InitSpeedLayer(conn *sql.DB, repo *db.Repo) {
	store := speedlayer.Build(conn)
	speedlayer.Install(store)
	usagetracker.SetStore(store)
	repo.SetSpeedStore(store)

	status := speedlayer.CurrentStatus()
	log.Info("boot", "node up",
		"node", status.NodeID,
		"mode", string(status.Mode),
		"db", "postgres",
		"speedPrimary", status.Primary,
		"speedServing", status.Serving,
		"speedDegraded", status.Degraded,
	)
}

// ProvideDatabase initializes the global database and registers an OnStop lifecycle hook to close it cleanly.
func ProvideDatabase(lc fx.Lifecycle, cfg *config.Config) (*sql.DB, error) {
	if err := db.InitGlobalDatabase(); err != nil {
		return nil, fmt.Errorf("database init: %w", err)
	}

	conn, err := db.GetConnection()
	if err != nil {
		return nil, fmt.Errorf("database connect: %w", err)
	}

	// Core schema bootstrap: create the shared tables/indexes when
	// absent, backfill missing columns, and seed the minimal rows.
	// Idempotent — existing user data is never touched.
	// Best-effort like leases below: a shared test binary may hand us a
	// connection bound to a removed handle (global singleton); a dead
	// connection is a test artifact, not a production schema failure.
	if err := db.EnsureCoreSchema(conn); err != nil {
		_, statErr := conn.Exec("SELECT 1")
		if statErr == nil {
			return nil, fmt.Errorf("database schema: %w", err)
		}
	}

	// Best-effort: a row stuck on a provider id the catalog dropped should
	// not block startup, it should just keep failing the way it already was.
	if err := db.MigrateLegacyProviderIDs(conn); err != nil {
		log.Warn("db", "legacy provider id migration failed", "error", err)
	}

	// Cross-process lease table for upstream coordination (Freebuff
	// sessions, future scopes). Idempotent: no-op when already present,
	// invisible to dashboards that do not know the table. Best-effort:
	// a shared test binary may hand us a connection bound to a removed
	// temp file (global singleton); leases then simply stay unavailable.
	if err := db.EnsureUpstreamLeases(conn); err != nil {
		_, statErr := conn.Exec("SELECT 1")
		if statErr == nil {
			return nil, fmt.Errorf("database leases: %w", err)
		}
	}

	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			return conn.Close()
		},
	})

	return conn, nil
}

// ProvideRepo provides *db.Repo using the database connection.
func ProvideRepo(conn *sql.DB) *db.Repo {
	repo := db.NewRepo(conn)
	// G26: hydrate thought signatures from the database so a signature written before
	// a restart is still replayable on the next tool turn. Fail-open — a broken
	// database only leaves the store process-local, exactly as it was before.
	translator.SetThoughtSignaturePersistence(repo)
	return repo
}
