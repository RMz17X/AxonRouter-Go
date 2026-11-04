package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Dialect names the SQL dialect a database handle speaks. WS2 (the
// PostgreSQL migration) introduces the second dialect; until the dialect
// sweep lands, most queries still speak SQLite, so every piece of code that
// needs to branch on the backend asks the handle — through DialectOf here,
// or through Repo.Dialect — instead of guessing from configuration.
type Dialect string

const (
	// DialectSQLite is the original modernc.org/sqlite backend.
	DialectSQLite Dialect = "sqlite"
	// DialectPostgres is the pgx stdlib backend selected by DATABASE_URL.
	DialectPostgres Dialect = "postgres"
)

// DialectOf reports the dialect of an arbitrary handle, decided by its
// driver. A handle carries its backend with it, which a package-level flag
// could not promise: tests open SQLite handles in the same process a
// production boot might have opened PostgreSQL in, and each must be read as
// what it is. Anything that is not the pgx stdlib driver is the SQLite
// backend, which is the only other driver this package opens.
func DialectOf(handle *sql.DB) Dialect {
	if handle == nil {
		return DialectSQLite
	}
	if strings.Contains(fmt.Sprintf("%T", handle.Driver()), "stdlib") {
		return DialectPostgres
	}
	return DialectSQLite
}

// pgMaxOpenConns bounds the PostgreSQL pool. The gateway deploys to a
// 1 vCPU box (AGENTS.md §4.G), and PostgreSQL is a shared server rather
// than a file this process owns, so the pool is sized for a handful of
// concurrent dashboard polls plus the usage write path — not for the
// hundreds of connections a default would permit across a cluster of
// these binaries.
const (
	pgMaxOpenConns = 10
	pgMaxIdleConns = 5
)

// pgStatementTimeoutMs is the per-statement ceiling applied to every
// pooled connection unless the operator set statement_timeout in
// DATABASE_URL itself. The usage window folds are the slowest statements
// this gateway runs; 30 s is far above their healthy cost and far below
// "a wedged query holds a pool slot forever".
const pgStatementTimeoutMs = "30000"

// OpenPostgresDatabase opens the PostgreSQL backend named by DATABASE_URL
// through the pgx stdlib driver, so the Repo layer keeps speaking
// database/sql exactly as it does over SQLite.
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
	handle.SetMaxOpenConns(pgMaxOpenConns)
	handle.SetMaxIdleConns(pgMaxIdleConns)
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

// databaseURL is the operator's PostgreSQL selection: a non-empty
// DATABASE_URL switches the global database from the SQLite file to
// PostgreSQL. DATA_DIR keeps holding the non-database state (jwt secret,
// model catalog) exactly as before either way.
func databaseURL() string {
	return strings.TrimSpace(os.Getenv("DATABASE_URL"))
}
