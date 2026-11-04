package db

import (
	"database/sql"
	"errors"
	"sync"
)

// The global connection and its open error, guarded by dbMu. The bool a
// separate "opened" flag would carry adds nothing the error does not.
var (
	dbMu       sync.Mutex
	dbInstance *sql.DB
	initErr    error
)

// InitGlobalDatabase opens the process-wide database connection once.
// Later calls are a no-op: production boots one gateway per process, and
// a second boot would silently reopen the same backend behind the first
// one's pooled connections. Tests are the one place that legitimately
// needs a fresh handle, which is what ResetGlobalDatabaseForTest is for.
//
// The backend is PostgreSQL, selected by DATABASE_URL — the only
// database this build speaks (P4 removed the embedded file backend).
// An unset or empty DATABASE_URL is a boot-fatal operator error, named
// here rather than surfacing later as a dead dashboard: there is no
// fallback database to fall back to.
func InitGlobalDatabase() error {
	dbMu.Lock()
	defer dbMu.Unlock()
	if dbInstance != nil {
		return initErr
	}
	dsn := databaseURL()
	if dsn == "" {
		initErr = errors.New("DATABASE_URL is not set: 9router-go requires a PostgreSQL connection string in DATABASE_URL (e.g. postgres://user:pass@host:5432/9router) — there is no embedded database in this build")
		return initErr
	}
	dbInstance, initErr = OpenPostgresDatabase(dsn)
	return initErr
}

// ResetGlobalDatabaseForTest closes and forgets the global connection so
// the next InitGlobalDatabase opens a real one. Without it, a test that
// boots app.DatabaseModule and stops its fx app closes the handle for
// good, and every later boot in the same binary inherits the closed
// handle and an already-cancelled shutdown context — which is exactly
// what makes `go test -shuffle` fail on internal/app in an
// order-dependent way.
//
// Production never calls this: closing the one real connection
// mid-process is precisely the failure mode the process-wide singleton
// exists to prevent. It mirrors shutdown.TestReset, which restores the
// sibling process-global.
func ResetGlobalDatabaseForTest(t interface{ Cleanup(func()) }) {
	t.Cleanup(func() { ResetGlobalDatabaseForTesting() })
	ResetGlobalDatabaseForTesting()
}

// ResetGlobalDatabaseForTesting is ResetGlobalDatabaseForTest without the
// testing.T dependency, for callers that manage their own teardown.
func ResetGlobalDatabaseForTesting() {
	dbMu.Lock()
	defer dbMu.Unlock()
	if dbInstance != nil {
		_ = dbInstance.Close()
	}
	dbInstance, initErr = nil, nil
}

// GetConnection returns the global database connection. When the boot
// init already failed, its error is the answer — it names the cause
// (an unset DATABASE_URL, an unreachable server) instead of a bare
// "not initialized" the operator cannot act on.
func GetConnection() (*sql.DB, error) {
	dbMu.Lock()
	defer dbMu.Unlock()
	if dbInstance == nil {
		if initErr != nil {
			return nil, initErr
		}
		return nil, errors.New("database not initialized, call InitGlobalDatabase first")
	}
	return dbInstance, nil
}
