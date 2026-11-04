package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// EnsurePostgresSchema is the PostgreSQL counterpart of EnsureCoreSchema:
// the same table set, the same additive-only contract, the same seeds,
// created when the PG backend is selected (EnsureCoreSchema branches on
// DialectOf). Definitions live in schema_pg_defs.go.
//
// Two deliberate omissions versus the SQLite path, both safe on a schema
// this function creates itself: the superseded-index drops (those indexes
// are never created here) and BackfillCompressionAnalytics (a repair of
// SQLite-era rows, written in SQLite dialect; a fresh PostgreSQL schema
// has no such rows — the data migrator owns repairs for imported ones).
//
// The bootstrap reads the existing catalog ONCE (pgCatalog) and diffs in
// memory rather than probing per column. The SQLite helper it mirrors
// pays one PRAGMA per column against a local file; against a server —
// possibly a network hop away — the same shape is one round trip per
// column for no benefit, and this package's own rule (§4.A of AGENTS.md)
// is one read, never N+1.
func EnsurePostgresSchema(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("postgres schema: nil db")
	}
	cat, err := loadPGCatalog(db)
	if err != nil {
		return err
	}
	for _, t := range pgCoreSchema() {
		if err := ensureTablePG(db, cat, t); err != nil {
			return err
		}
	}
	if err := ensurePartitionedUsageTables(db, cat); err != nil {
		return err
	}
	if err := ensureMonthlyPartitions(db, time.Now().UTC()); err != nil {
		return err
	}
	for _, t := range pgKeiRouterTables() {
		if err := ensureTablePG(db, cat, t); err != nil {
			return err
		}
	}
	if err := ensureDefaultPlanPG(db); err != nil {
		return err
	}
	for _, t := range pgUsageLedgerTables() {
		if err := ensureTablePG(db, cat, t); err != nil {
			return err
		}
	}
	// thoughtSignatureTables is declared with plain TEXT definitions that
	// are already valid PostgreSQL, so the same defs are reused as-is.
	for _, t := range thoughtSignatureTables {
		if err := ensureTablePG(db, cat, t); err != nil {
			return err
		}
	}
	if err := ensureAdditiveColumnsPG(db, cat); err != nil {
		return err
	}
	if _, err := db.Exec(
		`INSERT INTO _meta(key, value) VALUES('schemaVersion', '1') ON CONFLICT (key) DO NOTHING`,
	); err != nil {
		return fmt.Errorf("postgres schema: seed _meta: %w", err)
	}
	if _, err := db.Exec(
		`INSERT INTO settings(id, data) VALUES(1, '{}') ON CONFLICT (id) DO NOTHING`,
	); err != nil {
		return fmt.Errorf("postgres schema: seed settings: %w", err)
	}
	return nil
}

// pgCatalog is one snapshot of the schema this bootstrap diffs against:
// which tables exist and which columns each carries, keyed by the folded
// lowercase names the catalog stores. Objects created during the run are
// recorded into it as they are made, so later passes in the same boot see
// the state earlier passes produced.
type pgCatalog struct {
	tables  map[string]bool
	columns map[string]map[string]bool
}

func (c *pgCatalog) hasTable(table string) bool {
	return c.tables[strings.ToLower(table)]
}

func (c *pgCatalog) hasColumn(table, column string) bool {
	cols, ok := c.columns[strings.ToLower(table)]
	return ok && cols[strings.ToLower(column)]
}

// recordTable notes a table and its declared columns as existing.
func (c *pgCatalog) recordTable(t tableDef) {
	name := strings.ToLower(t.name)
	c.tables[name] = true
	cols, ok := c.columns[name]
	if !ok {
		cols = map[string]bool{}
		c.columns[name] = cols
	}
	for _, col := range t.columns {
		cols[strings.ToLower(col[0])] = true
	}
}

func (c *pgCatalog) recordColumn(table, column string) {
	name := strings.ToLower(table)
	cols, ok := c.columns[name]
	if !ok {
		cols = map[string]bool{}
		c.columns[name] = cols
	}
	cols[strings.ToLower(column)] = true
}

// loadPGCatalog reads the whole user schema's tables and columns in two
// statements, replacing the per-column probes a naive port of the SQLite
// helper would issue.
func loadPGCatalog(db *sql.DB) (*pgCatalog, error) {
	cat := &pgCatalog{
		tables:  map[string]bool{},
		columns: map[string]map[string]bool{},
	}
	rows, err := db.Query(
		`SELECT table_name FROM information_schema.tables
		 WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'`)
	if err != nil {
		return nil, fmt.Errorf("postgres schema: list tables: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("postgres schema: scan table list: %w", err)
		}
		cat.tables[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres schema: read table list: %w", err)
	}
	rows.Close()

	colRows, err := db.Query(
		`SELECT table_name, column_name FROM information_schema.columns
		 WHERE table_schema = current_schema()`)
	if err != nil {
		return nil, fmt.Errorf("postgres schema: list columns: %w", err)
	}
	defer colRows.Close()
	for colRows.Next() {
		var table, column string
		if err := colRows.Scan(&table, &column); err != nil {
			return nil, fmt.Errorf("postgres schema: scan column list: %w", err)
		}
		cols, ok := cat.columns[table]
		if !ok {
			cols = map[string]bool{}
			cat.columns[table] = cols
		}
		cols[column] = true
	}
	if err := colRows.Err(); err != nil {
		return nil, fmt.Errorf("postgres schema: read column list: %w", err)
	}
	return cat, nil
}

// ensurePartitionedUsageTables creates the two hot parents — usageHistory
// and requestDetails — as monthly RANGE-partitioned tables, the shape
// AxonRouter's schema.pg.js established for the same ledgers.
//
// The partition key is the timestamp column, kept TEXT in this step (the
// dialect sweep owns any later type migration). Partitioning still works
// because every writer stamps zero-padded RFC3339: for UTC 'Z' stamps the
// lexicographic order of the text IS chronological order, and a month
// bound of 'YYYY-MM-01' is a strict prefix bound — every timestamp of
// that month sorts >= the first of the month and < the first of the next.
// Partition pruning applies to the plain text range predicates the
// readers already use (timestamp >= '…' AND timestamp < '…').
//
// PostgreSQL requires a partitioned table's primary key to include the
// partition key, so both parents key on (id, timestamp) where SQLite keys
// on id alone — the same widening Axon's schema carries. The monthly
// partitions themselves are created by ensureMonthlyPartitions.
func ensurePartitionedUsageTables(db *sql.DB, cat *pgCatalog) error {
	parents := []tableDef{
		{
			name: "usageHistory",
			columns: [][2]string{
				{"id", "BIGINT GENERATED BY DEFAULT AS IDENTITY"},
				{"timestamp", "TEXT NOT NULL"},
				{"provider", "TEXT"},
				{"model", "TEXT"},
				{"connectionId", "TEXT"},
				{"apiKey", "TEXT"},
				{"endpoint", "TEXT"},
				{"promptTokens", "BIGINT DEFAULT 0"},
				{"completionTokens", "BIGINT DEFAULT 0"},
				{"cost", "DOUBLE PRECISION DEFAULT 0"},
				{"status", "TEXT"},
				{"tokens", "JSONB"},
				{"meta", "JSONB"},
				{"clientKey", "TEXT DEFAULT ''"},
			},
			primaryKey: "PRIMARY KEY (id, timestamp)",
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_uh_ts ON usageHistory(timestamp DESC)",
				"CREATE INDEX IF NOT EXISTS idx_uh_model ON usageHistory(model)",
				"CREATE INDEX IF NOT EXISTS idx_uh_conn ON usageHistory(connectionId)",
				"CREATE INDEX IF NOT EXISTS idx_uh_ts_prov ON usageHistory(timestamp DESC, provider)",
				"CREATE INDEX IF NOT EXISTS idx_uh_ts_model ON usageHistory(timestamp DESC, model)",
				"CREATE INDEX IF NOT EXISTS idx_uh_apikey_tok ON usageHistory(apiKey, id, promptTokens, completionTokens)",
				"CREATE INDEX IF NOT EXISTS idx_uh_clientkey_tok ON usageHistory(clientKey, id, promptTokens, completionTokens)",
				"CREATE INDEX IF NOT EXISTS idx_uh_prov_tok ON usageHistory(provider, id, promptTokens, completionTokens)",
				"CREATE INDEX IF NOT EXISTS idx_uh_analytics ON usageHistory(timestamp, provider, model, status, promptTokens, completionTokens, cost)",
			},
		},
		{
			name: "requestDetails",
			columns: [][2]string{
				{"id", "TEXT NOT NULL"},
				{"timestamp", "TEXT NOT NULL"},
				{"provider", "TEXT"},
				{"model", "TEXT"},
				{"connectionId", "TEXT"},
				{"status", "TEXT"},
				{"data", "JSONB NOT NULL"},
			},
			primaryKey: "PRIMARY KEY (id, timestamp)",
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_rd_ts ON requestDetails(timestamp DESC)",
				"CREATE INDEX IF NOT EXISTS idx_rd_provider ON requestDetails(provider)",
				"CREATE INDEX IF NOT EXISTS idx_rd_model ON requestDetails(model)",
				"CREATE INDEX IF NOT EXISTS idx_rd_conn ON requestDetails(connectionId)",
			},
		},
	}
	for _, t := range parents {
		defs := make([]string, 0, len(t.columns)+1)
		for _, c := range t.columns {
			defs = append(defs, c[0]+" "+c[1])
		}
		defs = append(defs, t.primaryKey)
		ddl := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s) PARTITION BY RANGE (timestamp)",
			t.name, strings.Join(defs, ", "))
		if _, err := db.Exec(ddl); err != nil {
			return fmt.Errorf("postgres schema: create partitioned table %s: %w", t.name, err)
		}
		cat.recordTable(t)
		// Declared on the parent, PostgreSQL maintains these indexes on
		// every current and future partition.
		for _, idx := range t.indexes {
			if _, err := db.Exec(idx); err != nil {
				return fmt.Errorf("postgres schema: create index on %s: %w", t.name, err)
			}
		}
	}
	return nil
}

// ensureMonthlyPartitions guarantees the current and next month's
// partitions exist for both hot tables, so a write at a month boundary
// never lands before its partition does. Names follow Axon's
// <parent>_yYYYYmMM pattern, over this schema's folded parent names.
// There is deliberately no DEFAULT partition: a row stamped outside the
// ensured window is a bug to surface, not traffic to file silently —
// the same stance Axon's schema takes.
func ensureMonthlyPartitions(db *sql.DB, now time.Time) error {
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		from := first.AddDate(0, i, 0)
		to := first.AddDate(0, i+1, 0)
		suffix := fmt.Sprintf("y%04dm%02d", from.Year(), int(from.Month()))
		bounds := fmt.Sprintf("FOR VALUES FROM ('%04d-%02d-01') TO ('%04d-%02d-01')",
			from.Year(), int(from.Month()), to.Year(), int(to.Month()))
		for _, parent := range []string{"usagehistory", "requestdetails"} {
			ddl := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s_%s PARTITION OF %s %s",
				parent, suffix, parent, bounds)
			if _, err := db.Exec(ddl); err != nil {
				return fmt.Errorf("postgres schema: ensure partition %s_%s: %w", parent, suffix, err)
			}
		}
	}
	return nil
}

// ensureDefaultPlanPG mirrors the default-plan seed in
// EnsureKeiRouterTables with PostgreSQL placeholders and conflict
// handling.
func ensureDefaultPlanPG(db *sql.DB) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(
		`INSERT INTO plans (id, tenant_id, name, description, limit_micros, limit_tokens, rpm_limit, tpm_limit, concurrency_limit, period, alert_pct, hard_cutoff, allowed_models, created_at, updated_at)
		 VALUES ('default', 'default', 'Default', 'Default plan for existing keys', 0, 0, 0, 0, 0, 'monthly', 80, 0, '', $1, $2)
		 ON CONFLICT (id) DO NOTHING`,
		now, now,
	); err != nil {
		return fmt.Errorf("postgres schema: seed default plan: %w", err)
	}
	return nil
}

// ensureAdditiveColumnsPG is EnsureAdditiveColumns for the PostgreSQL
// definitions (pgGoOnlyColumns), with the same skip-if-table-absent
// semantics, answered from the catalog snapshot.
func ensureAdditiveColumnsPG(db *sql.DB, cat *pgCatalog) error {
	for _, c := range pgGoOnlyColumns {
		if !cat.hasTable(c[0]) {
			continue
		}
		if cat.hasColumn(c[0], c[1]) {
			continue
		}
		if err := alterColumnAddPG(db, c[0], c[1], c[2]); err != nil {
			return err
		}
		cat.recordColumn(c[0], c[1])
	}
	return nil
}

// ensureTablePG is ensureTable with the catalog snapshot standing in for
// the SQLite helper's per-column PRAGMA probes.
func ensureTablePG(db *sql.DB, cat *pgCatalog, t tableDef) error {
	if !cat.hasTable(t.name) {
		defs := make([]string, 0, len(t.columns)+1)
		for _, c := range t.columns {
			defs = append(defs, c[0]+" "+c[1])
		}
		if t.primaryKey != "" {
			defs = append(defs, t.primaryKey)
		}
		if _, err := db.Exec(
			fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", t.name, strings.Join(defs, ", ")),
		); err != nil {
			return fmt.Errorf("postgres schema: create table %s: %w", t.name, err)
		}
		cat.recordTable(t)
	}
	for _, c := range t.columns {
		if cat.hasColumn(t.name, c[0]) {
			continue
		}
		if err := alterColumnAddPG(db, t.name, c[0], c[1]); err != nil {
			return err
		}
		cat.recordColumn(t.name, c[0])
	}
	for _, idx := range t.indexes {
		if _, err := db.Exec(idx); err != nil {
			return fmt.Errorf("postgres schema: create index on %s: %w", t.name, err)
		}
	}
	return nil
}

// alterColumnAddPG adds one column. The PRIMARY KEY / UNIQUE stripping
// matches the SQLite helper: those fragments are only legal at CREATE
// TABLE time, never in ADD COLUMN.
func alterColumnAddPG(db *sql.DB, table, column, definition string) error {
	safe := definition
	for _, frag := range []string{"PRIMARY KEY AUTOINCREMENT", "PRIMARY KEY", "UNIQUE"} {
		safe = strings.ReplaceAll(safe, frag, "")
	}
	safe = strings.TrimSpace(safe)
	safe = strings.Join(strings.Fields(safe), " ")
	if _, err := db.Exec(
		fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, safe),
	); err != nil {
		return fmt.Errorf("postgres schema: add column %s.%s: %w", table, column, err)
	}
	return nil
}
