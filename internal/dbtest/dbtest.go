package dbtest

import (
	"database/sql"
	"fmt"

	"9router/proxy/internal/db"
)

// SchemaStatements returns all CREATE TABLE statements used by 9router-go tests.
// Matches the canonical schema shared with the Next.js dashboard.
func SchemaStatements() []string {
	return []string{
		`CREATE TABLE apiKeys (
			id TEXT PRIMARY KEY,
			key TEXT UNIQUE NOT NULL,
			name TEXT,
			machineId TEXT,
			isActive INTEGER DEFAULT 1,
			createdAt TEXT NOT NULL
		)`,
		`CREATE TABLE providerConnections (
			id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			authType TEXT NOT NULL,
			name TEXT,
			email TEXT,
			priority INTEGER,
			isActive INTEGER DEFAULT 1,
			data TEXT NOT NULL,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		)`,
		`CREATE TABLE kv (
			scope TEXT NOT NULL,
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			PRIMARY KEY (scope, key)
		)`,
		`CREATE TABLE combos (
			id TEXT PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			kind TEXT,
			models TEXT NOT NULL,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		)`,
		`CREATE TABLE settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			data TEXT NOT NULL
		)`,
		`CREATE TABLE providerNodes (
			id TEXT PRIMARY KEY,
			type TEXT,
			name TEXT,
			data TEXT NOT NULL,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		)`,
		// usageHistory mirrors the production shape, id included: the reset marks a
		// token budget is measured from are usageHistory row ids, so a fixture
		// without the autoincrement primary key fails on the reset rather than on
		// the query the test is about.
		`CREATE TABLE IF NOT EXISTS usageHistory (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp TEXT,
			provider TEXT,
			model TEXT,
			connectionId TEXT,
			apiKey TEXT,
			endpoint TEXT,
			promptTokens INTEGER,
			completionTokens INTEGER,
			cost REAL,
			status TEXT,
			tokens TEXT,
			meta TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS usageDaily (
			dateKey TEXT PRIMARY KEY,
			data TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS requestDetails (
			id TEXT PRIMARY KEY,
			timestamp TEXT,
			provider TEXT,
			model TEXT,
			connectionId TEXT,
			status TEXT,
			data TEXT
		)`,
	}
}

// CreateTables creates all tables from SchemaStatements in the given database,
// then runs the core schema bootstrap, so a fixture matches the shape of a
// real database. Without this, queries that read the round-robin bookkeeping
// columns, the KeiRouter per-key columns, or the ledger carryover tables fail
// in tests while working in production. The bootstrap is the same one
// production boots with (internal/db's EnsureCoreSchema); the per-feature
// Ensure* helpers it replaced are gone with the file-backend schema.
func CreateTables(database *sql.DB) error {
	for _, stmt := range SchemaStatements() {
		if _, err := database.Exec(stmt); err != nil {
			return fmt.Errorf("create table: %w\nSQL: %s", err, stmt)
		}
	}
	return db.EnsureCoreSchema(database)
}
