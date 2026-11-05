// Package dbtest provisions throwaway PostgreSQL state for tests.
//
// There is no in-memory or file database anymore (AGENTS.md §5.C.4):
// database-backed tests run against a real PostgreSQL server. This
// package is the one provisioning helper they share. It hands out a
// *sql.DB pinned to a freshly created schema inside the database the
// operator named, so each test environment is isolated by schema
// rather than by database — one scratch database serves the whole
// suite, and cleanup is a single DROP SCHEMA ... CASCADE.
//
// The D1 port builds on this helper: the integration harness
// (internal/integration) is its first consumer, and the
// sqlite_legacy-quarantined per-package suites are expected to adopt
// it as they are ported, one package at a time.
//
// The package deliberately does not import testing: provisioning is a
// plain constructor with an error return, so any suite can shape the
// failure message its own way.
package dbtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// DSNEnvVar names the variable that points at the scratch database.
// DATABASE_URL is the fallback, matching AGENTS.md §5.C.4's contract
// that database tests run against the instance DATABASE_URL names.
const DSNEnvVar = "INTEGRATION_DATABASE_URL"

// ErrNoDatabase reports that no scratch database was configured (neither
// INTEGRATION_DATABASE_URL nor DATABASE_URL is set). It is a distinct
// state from a configured-but-unreachable database: owner rule
// (2026-11-06) — a test that needs a database SKIPS when none is
// configured, instead of failing. Callers check it with errors.Is and
// skip; every other provisioning error stays a failure.
var ErrNoDatabase = errors.New("dbtest: no database configured")

// BaseDSN resolves the provisioning DSN: INTEGRATION_DATABASE_URL,
// falling back to DATABASE_URL. When neither is set it returns
// ErrNoDatabase (wrapped with the variable names), because "connection
// refused" against an empty DSN is the failure this check exists to
// replace.
func BaseDSN() (string, error) {
	if dsn := strings.TrimSpace(os.Getenv(DSNEnvVar)); dsn != "" {
		return dsn, nil
	}
	if dsn := strings.TrimSpace(os.Getenv("DATABASE_URL")); dsn != "" {
		return dsn, nil
	}
	return "", fmt.Errorf("%w: neither %s nor DATABASE_URL is set", ErrNoDatabase, DSNEnvVar)
}

// NewSchemaHandle creates a fresh schema in the base database and
// returns a handle whose search_path is pinned to it, the schema
// name, and the cleanup that drops the schema and closes the handles.
//
// prefix names the schema family ("itest" for the integration
// harness); a random suffix keeps concurrent and repeated runs from
// colliding. The schema name is generated from a sanitised prefix
// plus hex, so quoting it in DDL is safe by construction.
func NewSchemaHandle(prefix string) (handle *sql.DB, schema string, cleanup func(), err error) {
	dsn, err := BaseDSN()
	if err != nil {
		return nil, "", nil, err
	}
	admin, err := openHandle(dsn)
	if err != nil {
		return nil, "", nil, err
	}

	schema, err = createSchema(admin, prefix)
	if err != nil {
		_ = admin.Close()
		return nil, "", nil, err
	}

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		_, _ = admin.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`)
		_ = admin.Close()
		return nil, "", nil, fmt.Errorf("dbtest: parse DSN: %w", err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = make(map[string]string)
	}
	cfg.RuntimeParams["search_path"] = schema
	handle = stdlib.OpenDB(*cfg)
	handle.SetMaxOpenConns(10)
	handle.SetMaxIdleConns(5)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	pingErr := handle.PingContext(ctx)
	cancel()
	if pingErr != nil {
		_ = handle.Close()
		_, _ = admin.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`)
		_ = admin.Close()
		return nil, "", nil, fmt.Errorf("dbtest: ping schema handle: %w", pingErr)
	}

	cleanup = func() {
		_ = handle.Close()
		_, _ = admin.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`)
		_ = admin.Close()
	}
	return handle, schema, cleanup, nil
}

// NewSchemaDSN creates a fresh schema like NewSchemaHandle but
// returns the base DSN re-pointed at it (the schema carried as the
// connection's search_path runtime parameter) instead of a handle.
// It exists for suites that boot the production wiring, which opens
// its own pool from DATABASE_URL: the suite sets that variable to the
// returned DSN and the application lands in the throwaway schema
// exactly as if it had connected to a throwaway database. Cleanup
// drops the schema; the caller must stop the application first, as a
// live pool holds the schema's objects open.
func NewSchemaDSN(prefix string) (dsn string, cleanup func(), err error) {
	base, err := BaseDSN()
	if err != nil {
		return "", nil, err
	}
	admin, err := openHandle(base)
	if err != nil {
		return "", nil, err
	}
	schema, err := createSchema(admin, prefix)
	if err != nil {
		_ = admin.Close()
		return "", nil, err
	}
	cleanup = func() {
		_, _ = admin.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`)
		_ = admin.Close()
	}
	// The search_path is appended to the DSN text explicitly: pgx's
	// ConnString does not serialize RuntimeParams, so a DSN built from
	// a parsed config silently loses the schema and the application
	// lands in the database's default schema instead (found by the
	// council round-1 suite triage, when bootfx's "fresh" schema
	// collided with rows a previous run had left in public). Both DSN
	// spellings pgx accepts carry the parameter in the open: as a URL
	// query parameter, or as a keyword in the space-separated form.
	if strings.HasPrefix(base, "postgres://") || strings.HasPrefix(base, "postgresql://") {
		sep := "?"
		if strings.Contains(base, "?") {
			sep = "&"
		}
		return base + sep + "search_path=" + schema, cleanup, nil
	}
	return base + " search_path=" + schema, cleanup, nil
}

// createSchema mints and creates one throwaway schema on an admin
// handle. The name is built from a sanitised prefix plus random hex,
// so quoting it in DDL is safe by construction.
func createSchema(admin *sql.DB, prefix string) (string, error) {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("dbtest: mint schema suffix: %w", err)
	}
	schema := sanitisePrefix(prefix) + "_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		return "", fmt.Errorf("dbtest: create schema %s: %w", schema, err)
	}
	return schema, nil
}

// openHandle opens and pings a plain handle on dsn. It mirrors the
// production constructor's shape (pgx stdlib, pinged before handout)
// without the production pool policy: a provisioning handle's only
// jobs are DDL and liveness.
func openHandle(dsn string) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("dbtest: parse DSN: %w", err)
	}
	handle := stdlib.OpenDB(*cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := handle.PingContext(ctx); err != nil {
		_ = handle.Close()
		return nil, fmt.Errorf("dbtest: connect to the scratch database: %w", err)
	}
	return handle, nil
}

// sanitisePrefix reduces a caller-chosen prefix to the identifier-safe
// alphabet the schema name is built from.
func sanitisePrefix(prefix string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(prefix) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "test"
	}
	return b.String()
}
