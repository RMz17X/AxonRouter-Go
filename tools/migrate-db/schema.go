package main

import (
	"context"
	_ "embed"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed schema_pg.sql
var schemaSQL string

// applySchema executes the embedded DDL (schema_pg.sql), which is a data
// transcription of the schema the application applies on a PostgreSQL
// boot. Every statement is IF NOT EXISTS / ADD COLUMN IF NOT EXISTS, so
// this is safe on a database the application has already booted against.
func applySchema(ctx context.Context, conn *pgx.Conn) error {
	stmts := splitSQL(schemaSQL)
	for i, stmt := range stmts {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("apply schema statement %d: %w\n%s", i+1, err, stmt)
		}
	}
	return nil
}

// splitSQL splits the embedded file into statements. The file's comments
// are all full-line `--` comments and no string literal contains a
// semicolon, so stripping comment lines and splitting on ';' is exact.
func splitSQL(sqlText string) []string {
	var kept []string
	for _, line := range strings.Split(sqlText, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept = append(kept, line)
	}
	var stmts []string
	for _, part := range strings.Split(strings.Join(kept, "\n"), ";") {
		if stmt := strings.TrimSpace(part); stmt != "" {
			stmts = append(stmts, stmt)
		}
	}
	return stmts
}

// partitionName mirrors ensureMonthlyPartitions in the application's
// schema_pg.go: <folded parent>_yYYYYmMM.
func partitionName(parentFolded, month string) string {
	return fmt.Sprintf("%s_y%sm%s", parentFolded, month[:4], month[5:7])
}

// monthBounds returns the [from, to) text bounds for a "YYYY-MM" month,
// the same strict prefix bounds the application uses.
func monthBounds(month string) (from, to string) {
	year, mon := month[:4], month[5:7]
	nextYear, nextMon := year, ""
	switch mon {
	case "12":
		nextYear = fmt.Sprintf("%04d", atoi(year)+1)
		nextMon = "01"
	default:
		nextMon = fmt.Sprintf("%02d", atoi(mon)+1)
	}
	return month + "-01", nextYear + "-" + nextMon + "-01"
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// ensurePartitions creates the monthly partitions for both hot tables:
// the current and next month (what the application guarantees at boot)
// plus every month in the given set (the months present in the source
// data — there is deliberately no DEFAULT partition, so a source row
// whose month has no partition would fail its insert, loudly, which is
// the application's own stance).
func ensurePartitions(ctx context.Context, conn *pgx.Conn, months map[string]bool) error {
	now := time.Now().UTC()
	cur := fmt.Sprintf("%04d-%02d", now.Year(), int(now.Month()))
	next := now.AddDate(0, 1, 0)
	months[cur] = true
	months[fmt.Sprintf("%04d-%02d", next.Year(), int(next.Month()))] = true

	sorted := make([]string, 0, len(months))
	for m := range months {
		sorted = append(sorted, m)
	}
	sort.Strings(sorted)
	for _, month := range sorted {
		from, to := monthBounds(month)
		for _, parent := range []string{"usagehistory", "requestdetails"} {
			ddl := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')",
				partitionName(parent, month), parent, from, to)
			if _, err := conn.Exec(ctx, ddl); err != nil {
				return fmt.Errorf("ensure partition %s: %w", partitionName(parent, month), err)
			}
		}
	}
	return nil
}

// logicalTables returns the target's copyable tables: every table in
// the current schema except partition children (a partitioned parent
// is copied as one logical table; its children are storage, not tables
// of record). Partitioned parents carry relkind 'p', ordinary tables
// and partition children 'r' — both kinds belong here, with the
// pg_inherits filter removing the children.
func logicalTables(ctx context.Context, conn *pgx.Conn) (map[string]bool, error) {
	rows, err := conn.Query(ctx,
		`SELECT c.relname FROM pg_class c
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p')
		   AND NOT EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid = c.oid)`)
	if err != nil {
		return nil, fmt.Errorf("list target tables: %w", err)
	}
	defer rows.Close()
	tables := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan target table: %w", err)
		}
		tables[name] = true
	}
	return tables, rows.Err()
}

// tableCount counts a target table (a partitioned parent counts all of
// its partitions).
func tableCount(ctx context.Context, conn *pgx.Conn, folded string) (int64, error) {
	var n int64
	err := conn.QueryRow(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, folded)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count %s: %w", folded, err)
	}
	return n, nil
}

// truncateTables empties every listed target table. Truncating a
// partitioned parent truncates all of its partitions; there are no
// foreign keys in this schema, so no CASCADE is needed.
func truncateTables(ctx context.Context, conn *pgx.Conn, folded []string) error {
	if len(folded) == 0 {
		return nil
	}
	quoted := make([]string, len(folded))
	for i, t := range folded {
		quoted[i] = pgx.Identifier{t}.Sanitize()
	}
	if _, err := conn.Exec(ctx, "TRUNCATE TABLE "+strings.Join(quoted, ", ")); err != nil {
		return fmt.Errorf("truncate target: %w", err)
	}
	return nil
}

// resetIdentity advances each identity sequence past the greatest copied
// id, so the application's own inserts after the migration cannot
// collide with migrated rows. Tables left empty restart at 1.
func resetIdentity(ctx context.Context, conn *pgx.Conn, folded string) error {
	var maxID int64
	if err := conn.QueryRow(ctx,
		fmt.Sprintf(`SELECT COALESCE(MAX(id), 0) FROM "%s"`, folded)).Scan(&maxID); err != nil {
		return fmt.Errorf("max id %s: %w", folded, err)
	}
	if maxID > 0 {
		_, err := conn.Exec(ctx,
			`SELECT setval(pg_get_serial_sequence($1, 'id'), $2, true)`, folded, maxID)
		if err != nil {
			return fmt.Errorf("setval %s: %w", folded, err)
		}
		return nil
	}
	_, err := conn.Exec(ctx,
		`SELECT setval(pg_get_serial_sequence($1, 'id'), 1, false)`, folded)
	if err != nil {
		return fmt.Errorf("setval %s: %w", folded, err)
	}
	return nil
}

// applySeeds writes the seed rows the application guarantees on boot,
// with the application's own statements (ON CONFLICT DO NOTHING). The
// tool runs them after the copy: whatever the source carried wins, and
// the seeds only fill a gap the source left.
func applySeeds(ctx context.Context, conn *pgx.Conn) error {
	if _, err := conn.Exec(ctx,
		`INSERT INTO _meta(key, value) VALUES('schemaVersion', '1') ON CONFLICT (key) DO NOTHING`); err != nil {
		return fmt.Errorf("seed _meta: %w", err)
	}
	if _, err := conn.Exec(ctx,
		`INSERT INTO settings(id, data) VALUES(1, '{}') ON CONFLICT (id) DO NOTHING`); err != nil {
		return fmt.Errorf("seed settings: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := conn.Exec(ctx,
		`INSERT INTO plans (id, tenant_id, name, description, limit_micros, limit_tokens, rpm_limit, tpm_limit, concurrency_limit, period, alert_pct, hard_cutoff, allowed_models, created_at, updated_at)
		 VALUES ('default', 'default', 'Default', 'Default plan for existing keys', 0, 0, 0, 0, 0, 'monthly', 80, 0, '', $1, $2)
		 ON CONFLICT (id) DO NOTHING`, now, now); err != nil {
		return fmt.Errorf("seed default plan: %w", err)
	}
	return nil
}
