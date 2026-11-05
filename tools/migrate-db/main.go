// migrate-db is the one-shot SQLite -> PostgreSQL data migrator for
// axonrouter-go (plan §5/P2). It is a separate module on purpose: its
// SQLite driver must never enter the runtime's module graph, and the
// runtime never links it. It reads both schemas as data — the SQLite
// file's catalog and the PostgreSQL DDL transcribed in schema_pg.sql —
// and imports none of the application's packages.
//
// Usage:
//
//	migrate-db --sqlite <path to data.sqlite> --pg <DATABASE_URL> [--truncate]
//
// Target rule: the tool creates the schema if needed, then refuses to
// copy into a target where any table it would write already holds rows,
// unless --truncate is given (which empties those tables first). A
// second run against a migrated target therefore refuses and leaves the
// data untouched; a --truncate run rebuilds the identical end state.
// Both properties together are the tool's idempotency contract.
//
// Rows that cannot be represented in PostgreSQL — above all legacy rows
// whose JSON payload is malformed — are skipped and reported, never
// fatal and never mangled. The closing verification pass recounts the
// target and fails (exit 1) unless every table satisfies
// target = source - skipped.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"modernc.org/sqlite"
)

// hotTables are the partitioned ledgers: they get partition routing and
// min/max timestamp verification on top of the per-table counts.
var hotTables = []string{"usagehistory", "requestdetails"}

// identityTables hold identity columns whose sequences must be advanced
// past the copied ids.
var identityTables = []string{"usagehistory", "compressionanalytics", "mcp_tool_audit"}

var monthPattern = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "migrate-db: %v\n", err)
		os.Exit(1)
	}
}

// isSourceRecoveryError reports whether err is one of the two SQLite
// classes a WAL-mode source produces when its directory is not
// writable: SQLITE_CANTOPEN (14, "unable to open database file" — the
// -shm/-wal sidecars cannot be created, e.g. a read-only mount) and
// SQLITE_READONLY_RECOVERY (1544 — the file opens but WAL recovery
// cannot write). Both mean the open failed on the sidecars, not on the
// database itself.
func isSourceRecoveryError(err error) bool {
	var serr *sqlite.Error
	if !errors.As(err, &serr) {
		return false
	}
	return serr.Code() == 14 || serr.Code() == 1544
}

func run() error {
	sqlitePath := flag.String("sqlite", "", "path to the source data.sqlite (read-only)")
	pgURL := flag.String("pg", "", "PostgreSQL connection URL (DATABASE_URL)")
	truncate := flag.Bool("truncate", false,
		"empty the target tables before copying (required to re-run against a non-empty target)")
	flag.Parse()
	if *sqlitePath == "" || *pgURL == "" {
		flag.Usage()
		return fmt.Errorf("both --sqlite and --pg are required")
	}
	ctx := context.Background()

	src, err := sql.Open("sqlite", "file:"+*sqlitePath+"?mode=ro")
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer src.Close()
	if err := src.PingContext(ctx); err != nil {
		if isSourceRecoveryError(err) {
			// On a read-only open these classes almost always mean the
			// source is a WAL-mode database in a directory that is not
			// writable: opening it needs to create the -shm/-wal
			// recovery sidecars even in mode=ro. The raw driver error
			// names none of that (review cycle 3, C-3).
			return fmt.Errorf("open source %s: %w — a WAL-mode source needs a writable directory for its -shm/-wal recovery sidecars; mount the legacy directory writable", *sqlitePath, err)
		}
		return fmt.Errorf("open source %s: %w", *sqlitePath, err)
	}

	conn, err := pgx.Connect(ctx, *pgURL)
	if err != nil {
		return fmt.Errorf("connect target: %w", err)
	}
	defer conn.Close(ctx)

	srcTables, err := sourceTables(ctx, src)
	if err != nil {
		return err
	}

	fmt.Println("== schema ==")
	if err := applySchema(ctx, conn); err != nil {
		return err
	}
	months, err := sourceMonths(ctx, src)
	if err != nil {
		return err
	}
	srcMonthCount := len(months)
	if err := ensurePartitions(ctx, conn, months); err != nil {
		return err
	}
	fmt.Printf("schema applied; partitions ensured for %d source month(s) plus current/next\n", srcMonthCount)

	targets, err := logicalTables(ctx, conn)
	if err != nil {
		return err
	}

	// Build the copy list: the known order first, then any remaining
	// source table that has a target. Source tables with no target are
	// reported, not silently dropped.
	inOrder := map[string]bool{}
	var copyList []string
	byFolded := map[string]string{}
	for _, t := range srcTables {
		byFolded[strings.ToLower(t)] = t
	}
	for _, folded := range copyOrder {
		if srcName, ok := byFolded[folded]; ok && targets[folded] {
			copyList = append(copyList, srcName)
			inOrder[folded] = true
		}
	}
	var extra []string
	for _, t := range srcTables {
		folded := strings.ToLower(t)
		if targets[folded] && !inOrder[folded] {
			extra = append(extra, t)
		}
	}
	sort.Strings(extra)
	copyList = append(copyList, extra...)

	var noTarget []string
	for _, t := range srcTables {
		if !targets[strings.ToLower(t)] {
			noTarget = append(noTarget, t)
		}
	}

	// Non-empty target rule.
	var nonEmpty []string
	for _, t := range copyList {
		n, err := tableCount(ctx, conn, strings.ToLower(t))
		if err != nil {
			return err
		}
		if n > 0 {
			nonEmpty = append(nonEmpty, fmt.Sprintf("%s (%d rows)", t, n))
		}
	}
	if len(nonEmpty) > 0 && !*truncate {
		return fmt.Errorf("target is not empty and --truncate was not given; refusing to copy into: %s",
			strings.Join(nonEmpty, ", "))
	}
	if len(nonEmpty) > 0 {
		folded := make([]string, len(copyList))
		for i, t := range copyList {
			folded[i] = strings.ToLower(t)
		}
		if err := truncateTables(ctx, conn, folded); err != nil {
			return err
		}
		fmt.Printf("== target truncated (%d tables) ==\n", len(copyList))
	}

	fmt.Println("== copy ==")
	results := make([]*tableResult, 0, len(copyList))
	for _, t := range copyList {
		res, err := copyTable(ctx, src, conn, t)
		if err != nil {
			printResults(results)
			return err
		}
		results = append(results, res)
	}
	for _, folded := range identityTables {
		if targets[folded] {
			if err := resetIdentity(ctx, conn, folded); err != nil {
				return err
			}
		}
	}
	if err := applySeeds(ctx, conn); err != nil {
		return err
	}

	printResults(results)
	// A source table with no target is reported, never silent. For a
	// table this tool knows (copyOrder) holding rows it is also a
	// verification failure: the application's own tables must all land.
	// A foreign table (a legacy shared database can hold other writers'
	// tables) stays report-only.
	var strictMisses []string
	for _, t := range noTarget {
		n, _ := sourceCount(ctx, src, t)
		fmt.Printf("no target table for source table %s (%d rows) — not copied\n", t, n)
		if n > 0 && inCopyOrder(strings.ToLower(t)) {
			strictMisses = append(strictMisses,
				fmt.Sprintf("%s: %d source rows have no target table", t, n))
		}
	}

	fmt.Println("== verify ==")
	failures, err := verify(ctx, src, conn, results)
	if err != nil {
		return err
	}
	failures = append(failures, strictMisses...)
	for _, f := range failures {
		fmt.Println("FAIL:", f)
	}
	if len(failures) > 0 {
		return fmt.Errorf("verification failed: %d mismatch(es)", len(failures))
	}
	fmt.Println("verification passed: every table satisfies target = source - skipped")
	return nil
}

// inCopyOrder reports whether a folded table name is one this tool
// knows how to copy.
func inCopyOrder(folded string) bool {
	for _, t := range copyOrder {
		if t == folded {
			return true
		}
	}
	return false
}

// sourceMonths collects the distinct YYYY-MM prefixes of the hot
// tables' timestamps, so partitions exist for every month the copy
// will route to.
func sourceMonths(ctx context.Context, src *sql.DB) (map[string]bool, error) {
	months := map[string]bool{}
	existing, err := sourceTables(ctx, src)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, t := range existing {
		have[strings.ToLower(t)] = true
	}
	for _, folded := range hotTables {
		if !have[folded] {
			continue
		}
		rows, err := src.QueryContext(ctx,
			fmt.Sprintf(`SELECT DISTINCT substr(timestamp, 1, 7) FROM "%s"`, folded))
		if err != nil {
			return nil, fmt.Errorf("read %s months: %w", folded, err)
		}
		for rows.Next() {
			var m string
			if err := rows.Scan(&m); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan %s month: %w", folded, err)
			}
			if monthPattern.MatchString(m) {
				months[m] = true
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate %s months: %w", folded, err)
		}
		rows.Close()
	}
	return months, nil
}

// printResults renders the per-table copy report, with rows/sec so the
// bulk tables' throughput is on the record.
func printResults(results []*tableResult) {
	fmt.Printf("%-26s %10s %10s %8s %10s\n", "table", "source", "copied", "skipped", "rows/sec")
	for _, r := range results {
		rps := ""
		if r.Duration > 0 && r.Copied > 0 {
			rps = fmt.Sprintf("%.0f", float64(r.Copied)/r.Duration.Seconds())
		}
		fmt.Printf("%-26s %10d %10d %8d %10s\n", r.Table, r.Source, r.Copied, r.Skipped, rps)
		for _, s := range r.Skips {
			fmt.Printf("  skipped %s: row %s, column %s: %s\n", r.Table, s.RowID, s.Column, s.Reason)
		}
		if extra := r.Skipped - int64(len(r.Skips)); extra > 0 {
			fmt.Printf("  … and %d more skipped rows in %s (reasons as above)\n", extra, r.Table)
		}
		for _, c := range r.SourceOnlyColumns {
			fmt.Printf("  note: source column %s.%s has no target counterpart — not copied\n", r.Table, c)
		}
		if r.TargetOnlyColumns > 0 {
			fmt.Printf("  note: %d target column(s) of %s not present in the source took their defaults\n",
				r.TargetOnlyColumns, r.Table)
		}
	}
}

// verify is the closing pass: per-table counts on the target must equal
// source minus skipped; the hot tables also compare min/max timestamps
// and report their per-partition counts (the routing proof).
func verify(ctx context.Context, src *sql.DB, conn *pgx.Conn, results []*tableResult) ([]string, error) {
	var failures []string
	for _, r := range results {
		folded := strings.ToLower(r.Table)
		got, err := tableCount(ctx, conn, folded)
		if err != nil {
			return nil, err
		}
		if want := r.Source - r.Skipped; got != want {
			failures = append(failures,
				fmt.Sprintf("%s: target holds %d rows, want %d (source %d - skipped %d)",
					r.Table, got, want, r.Source, r.Skipped))
		}
	}
	for _, folded := range hotTables {
		var res *tableResult
		for _, r := range results {
			if strings.ToLower(r.Table) == folded {
				res = r
				break
			}
		}
		if res == nil || res.Source == 0 {
			continue
		}
		if res.Skipped == 0 {
			srcMin, srcMax, err := srcMinMax(ctx, src, res.Table)
			if err != nil {
				return nil, err
			}
			pgMin, pgMax, err := pgMinMax(ctx, conn, folded)
			if err != nil {
				return nil, err
			}
			if srcMin != pgMin || srcMax != pgMax {
				failures = append(failures, fmt.Sprintf(
					"%s: timestamp range differs — source [%s .. %s], target [%s .. %s]",
					res.Table, srcMin, srcMax, pgMin, pgMax))
			} else {
				fmt.Printf("%s timestamp range verified: %s .. %s\n", res.Table, srcMin, srcMax)
			}
		}
		parts, err := partitionCounts(ctx, conn, folded)
		if err != nil {
			return nil, err
		}
		var sum int64
		for name, n := range parts {
			fmt.Printf("  partition %-28s %d rows\n", name, n)
			sum += n
		}
		if parent, err := tableCount(ctx, conn, folded); err != nil {
			return nil, err
		} else if sum != parent {
			failures = append(failures, fmt.Sprintf(
				"%s: partitions hold %d rows but the parent counts %d", res.Table, sum, parent))
		}
	}
	return failures, nil
}

func srcMinMax(ctx context.Context, db *sql.DB, table string) (string, string, error) {
	var lo, hi sql.NullString
	err := db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT MIN(timestamp), MAX(timestamp) FROM "%s"`, table)).Scan(&lo, &hi)
	if err != nil {
		return "", "", fmt.Errorf("source min/max %s: %w", table, err)
	}
	return lo.String, hi.String, nil
}

func pgMinMax(ctx context.Context, conn *pgx.Conn, folded string) (string, string, error) {
	var lo, hi *string
	err := conn.QueryRow(ctx,
		fmt.Sprintf(`SELECT MIN(timestamp), MAX(timestamp) FROM "%s"`, folded)).Scan(&lo, &hi)
	if err != nil {
		return "", "", fmt.Errorf("target min/max %s: %w", folded, err)
	}
	return strVal(lo), strVal(hi), nil
}

func strVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// partitionCounts counts every partition of a partitioned parent
// directly, child by child.
func partitionCounts(ctx context.Context, conn *pgx.Conn, parentFolded string) (map[string]int64, error) {
	rows, err := conn.Query(ctx,
		`SELECT child.relname FROM pg_inherits i
		 JOIN pg_class parent ON parent.oid = i.inhparent
		 JOIN pg_class child ON child.oid = i.inhrelid
		 JOIN pg_namespace n ON n.oid = parent.relnamespace
		 WHERE n.nspname = current_schema() AND parent.relname = $1
		 ORDER BY child.relname`, parentFolded)
	if err != nil {
		return nil, fmt.Errorf("list partitions of %s: %w", parentFolded, err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan partition: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate partitions: %w", err)
	}
	rows.Close()
	counts := map[string]int64{}
	for _, name := range names {
		n, err := tableCount(ctx, conn, name)
		if err != nil {
			return nil, err
		}
		counts[name] = n
	}
	return counts, nil
}
