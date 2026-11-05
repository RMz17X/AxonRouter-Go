package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// EnsureCoreSchema creates the gateway's tables/indexes when absent,
// backfills missing columns on existing databases, and seeds the
// minimal rows a fresh dashboard needs. Definitions live in
// schema_pg_defs.go. Safe to call on every startup: every statement is
// IF NOT EXISTS / ON CONFLICT DO NOTHING / column-presence-checked, so
// existing user data is never touched.
//
// The bootstrap reads the existing catalog ONCE (pgCatalog) and diffs
// in memory rather than probing per column: against a server —
// possibly a network hop away — a probe per column is one round trip
// per column for no benefit, and this package's own rule (§4.A of
// AGENTS.md) is one read, never N+1.
func EnsureCoreSchema(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("postgres schema: nil db")
	}
	cat, err := loadPGCatalog(db)
	if err != nil {
		return err
	}
	// The bootstrap runs in three phases, and the phase boundary is the
	// load-bearing part: NO index is created before every column exists.
	// An index may reference an additive column (pgGoOnlyColumns — e.g.
	// apiKeys.lookupHash, providerConnections.lastUsedAt), and those are
	// only added in phase 2; an index executed during table creation
	// against such a column aborts the boot on a fresh database
	// (SQLSTATE 42703 — the idx_pc_last_used_at incident, fragment 945).
	// Declaring every index in its table's def and executing them all in
	// phase 3 makes that failure class impossible by construction.
	//
	// Phase 1 — tables and their declared columns.
	// thoughtSignatureTables is declared with plain TEXT definitions that
	// are already valid PostgreSQL, so the same defs are reused as-is.
	groups := [][]tableDef{
		pgCoreSchema(),
		pgKeiRouterTables(),
		pgUsageLedgerTables(),
		thoughtSignatureTables,
	}
	for _, group := range groups {
		for _, t := range group {
			if err := ensureTableColumnsPG(db, cat, t); err != nil {
				return err
			}
		}
	}
	if err := ensurePartitionedUsageTables(db, cat); err != nil {
		return err
	}
	if err := ensureMonthlyPartitions(db, time.Now().UTC()); err != nil {
		return err
	}
	if err := ensureDefaultPlanPG(db); err != nil {
		return err
	}
	// Phase 2 — additive columns.
	if err := ensureAdditiveColumnsPG(db, cat); err != nil {
		return err
	}
	// Phase 3 — every declared index, now that every column exists.
	for _, group := range groups {
		for _, t := range group {
			if err := ensureTableIndexesPG(db, t); err != nil {
				return err
			}
		}
	}
	if err := ensurePartitionedIndexesPG(db); err != nil {
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
// statements, instead of probing per column.
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
// partition key, so both parents key on (id, timestamp) rather than id
// alone — the same widening Axon's schema carries. The monthly
// partitions themselves are created by ensureMonthlyPartitions.
func partitionedUsageParents() []tableDef {
	return []tableDef{
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
}

// ensurePartitionedUsageTables creates the two partitioned parents
// (declared columns only — their indexes belong to the index phase,
// see EnsureCoreSchema).
func ensurePartitionedUsageTables(db *sql.DB, cat *pgCatalog) error {
	for _, t := range partitionedUsageParents() {
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
	}
	return nil
}

// ensurePartitionedIndexesPG creates the partitioned parents' declared
// indexes in the index phase. Declared on the parent, PostgreSQL
// maintains these indexes on every current and future partition.
func ensurePartitionedIndexesPG(db *sql.DB) error {
	for _, t := range partitionedUsageParents() {
		if err := ensureTableIndexesPG(db, t); err != nil {
			return err
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

// ensureDefaultPlanPG seeds the default plan row with PostgreSQL
// placeholders and conflict handling.
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

// ensureAdditiveColumnsPG applies the additive Go-only columns
// (pgGoOnlyColumns) with skip-if-table-absent semantics, answered from
// the catalog snapshot.
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

// ensureTableColumnsPG creates or extends one table's declared
// columns, with the catalog snapshot standing in for per-column
// probes. Indexes are NOT created here: they belong to the index
// phase (ensureTableIndexesPG), which runs after the additive
// columns exist — see EnsureCoreSchema.
func ensureTableColumnsPG(db *sql.DB, cat *pgCatalog, t tableDef) error {
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
	return nil
}

// ensureTableIndexesPG creates one table's declared indexes. It runs
// in EnsureCoreSchema's index phase, after every declared and
// additive column exists, so an index may reference any column of its
// table — including additive ones such as apiKeys.lookupHash (the
// hashed-key auth lookup, FindApiKeyByLookup's index).
func ensureTableIndexesPG(db *sql.DB, t tableDef) error {
	for _, idx := range t.indexes {
		if _, err := db.Exec(idx); err != nil {
			return fmt.Errorf("postgres schema: create index on %s: %w", t.name, err)
		}
	}
	return nil
}

// tableExistsOnDB reports whether a table is present, probed through
// the catalog (to_regclass, which folds the name the same way the DDL
// did, so a camelCase probe name matches the folded catalog entry)
// rather than depending on rewordable driver error strings. Runtime
// readers call this too (the ledger prune's providerNodes probe), not
// only the bootstrap.
func tableExistsOnDB(db *sql.DB, table string) (bool, error) {
	var exists bool
	err := db.QueryRow(
		`SELECT to_regclass($1) IS NOT NULL`, table,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("core schema: inspect table %s: %w", table, err)
	}
	return exists, nil
}

// alterColumnAddPG adds one column. The PRIMARY KEY / UNIQUE fragments
// are stripped: they are only legal at CREATE TABLE time, never in
// ADD COLUMN.
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
