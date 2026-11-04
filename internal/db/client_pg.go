package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// PostgreSQL pool sizing (Efficiency Mandate: deliberate config, not a
// driver default). The defaults are sized for the deployment target in
// AGENTS.md §4.G — a 1 vCPU box whose PostgreSQL is a shared server:
//
//   - 10 open connections per node covers the real concurrency shape:
//     one usage write + a couple of reads per in-flight chat request,
//     plus the dashboard's poll set (stats fold, connections, keys)
//     bursting every few seconds. Measured demand in the P1 smokes
//     never queued at 10; going higher buys nothing on 1 vCPU and
//     spends the server's connection budget — PostgreSQL's own default
//     max_connections is 100, so a 4-node cluster at 10 stays at 40%
//     of it with room for the migrator and operator shells.
//   - 5 idle keeps the dashboard's poll cadence off the dial path
//     without holding the full pool open between polls.
//
// PG_POOL_MAX_OPEN / PG_POOL_MAX_IDLE override for larger nodes or a
// bigger shared server; idle is clamped to open, and non-positive or
// unparseable values fall back to the defaults.
const (
	pgDefaultMaxOpenConns = 10
	pgDefaultMaxIdleConns = 5
)

func pgPoolSizes() (maxOpen, maxIdle int) {
	maxOpen = envPositiveInt("PG_POOL_MAX_OPEN", pgDefaultMaxOpenConns)
	maxIdle = envPositiveInt("PG_POOL_MAX_IDLE", pgDefaultMaxIdleConns)
	if maxIdle > maxOpen {
		maxIdle = maxOpen
	}
	return maxOpen, maxIdle
}

func envPositiveInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// pgStatementTimeoutMs is the per-statement ceiling applied to every
// pooled connection unless the operator set statement_timeout in
// DATABASE_URL itself. The usage window folds are the slowest statements
// this gateway runs; 30 s is far above their healthy cost and far below
// "a wedged query holds a pool slot forever".
const pgStatementTimeoutMs = "30000"

// OpenPostgresDatabase opens the PostgreSQL backend named by DATABASE_URL
// through the pgx stdlib driver, so the Repo layer keeps speaking
// database/sql.
//
// The connection is pinged before it is handed out: a DATABASE_URL that
// cannot reach its server is an operator error that must fail the boot
// loudly here, naming the variable, rather than surfacing as a dead
// dashboard later. The DSN itself is never written into an error — it
// carries the password.
func OpenPostgresDatabase(dsn string) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: DATABASE_URL is not a parseable connection string: %w", err)
	}
	if _, ok := cfg.RuntimeParams["statement_timeout"]; !ok {
		cfg.RuntimeParams["statement_timeout"] = pgStatementTimeoutMs
	}

	handle := stdlib.OpenDB(*cfg)
	maxOpen, maxIdle := pgPoolSizes()
	handle.SetMaxOpenConns(maxOpen)
	handle.SetMaxIdleConns(maxIdle)
	handle.SetConnMaxLifetime(30 * time.Minute)
	handle.SetConnMaxIdleTime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := handle.PingContext(ctx); err != nil {
		_ = handle.Close()
		return nil, fmt.Errorf("postgres: DATABASE_URL connection failed: %w", err)
	}
	return handle, nil
}

// databaseURL is the operator's PostgreSQL selection: DATABASE_URL is
// the one and only database configuration this build reads.
func databaseURL() string {
	return strings.TrimSpace(os.Getenv("DATABASE_URL"))
}
