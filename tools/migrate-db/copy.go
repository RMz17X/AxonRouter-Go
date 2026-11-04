package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// pgColumn is one target column as information_schema reports it (the
// name is already the folded lowercase PostgreSQL stores).
type pgColumn struct {
	name     string
	dataType string // text, bigint, integer, jsonb, bytea, double precision, ...
	nullable bool
}

// tableResult is the per-table line of the migration report.
type tableResult struct {
	Table    string
	Source   int64
	Copied   int64
	Skipped  int64
	Skips    []skipRecord // capped; Skipped holds the true total
	Duration time.Duration

	// SourceOnlyColumns are source columns with no target counterpart
	// (their data has nowhere to land — named, never silent).
	SourceOnlyColumns []string
	// TargetOnlyColumns counts target columns the source does not carry;
	// they take their DDL defaults, which is the additive-schema contract.
	TargetOnlyColumns int
}

type skipRecord struct {
	RowID  string // first-column value of the skipped row, when readable
	Column string
	Reason string
}

// alwaysIdentityTables hold a GENERATED ALWAYS identity column:
// PostgreSQL rejects explicit values there except through
// INSERT ... OVERRIDING SYSTEM VALUE, which COPY cannot express. These
// tables are audit/log shaped (small by nature) and take the batched
// insert path; every other table takes CopyFrom.
var alwaysIdentityTables = map[string]bool{
	"mcp_tool_audit": true,
}

// copyOrder is the dependency-safe copy sequence: lookup/seed tables
// first, entities before the rows that name them, the two hot ledger
// tables last. The schema declares no foreign keys, so this order is
// about report readability and failure locality, not constraint
// satisfaction; any source table with a target that is not listed here
// is appended after these, alphabetically.
var copyOrder = []string{
	"_meta", "settings", "plans",
	"providernodes", "providerconnections", "proxypools", "proxygroups",
	"apikeys", "api_key_model_access", "combos", "kv",
	"guardrail_policies", "guardrail_logs",
	"usagedaily", "usageledgercarryover", "providerledgercarryover",
	"thoughtsignatures", "upstream_leases",
	"semanticcacheentries", "files", "batches",
	"compressionanalytics", "mcp_tool_audit",
	"usagehistory", "requestdetails",
}

// sourceTables lists the SQLite tables to consider, excluding SQLite's
// own bookkeeping (sqlite_sequence and friends).
func sourceTables(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list source tables: %w", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan source table: %w", err)
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

// sourceColumns returns the source table's column names in declared
// order (PRAGMA table_info).
func sourceColumns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info("%s")`, table))
	if err != nil {
		return nil, fmt.Errorf("inspect source %s: %w", table, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return nil, fmt.Errorf("scan source %s columns: %w", table, err)
		}
		cols = append(cols, name)
	}
	return cols, rows.Err()
}

// targetColumns returns the target table's columns in ordinal order.
func targetColumns(ctx context.Context, conn *pgx.Conn, folded string) ([]pgColumn, error) {
	rows, err := conn.Query(ctx,
		`SELECT column_name, data_type, is_nullable FROM information_schema.columns
		 WHERE table_schema = current_schema() AND table_name = $1
		 ORDER BY ordinal_position`, folded)
	if err != nil {
		return nil, fmt.Errorf("inspect target %s: %w", folded, err)
	}
	defer rows.Close()
	var cols []pgColumn
	for rows.Next() {
		var c pgColumn
		var nullable string
		if err := rows.Scan(&c.name, &c.dataType, &nullable); err != nil {
			return nil, fmt.Errorf("scan target %s columns: %w", folded, err)
		}
		c.nullable = nullable == "YES"
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// sourceCount counts a source table.
func sourceCount(ctx context.Context, db *sql.DB, table string) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, table)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count source %s: %w", table, err)
	}
	return n, nil
}

// copyTable copies one table. Rows whose values cannot be represented
// in the target — above all a malformed JSON payload for a jsonb
// column — are skipped and recorded in the result, never fatal and
// never mangled (the plan's P2 rule for legacy shared-DB rows).
func copyTable(ctx context.Context, src *sql.DB, conn *pgx.Conn, srcTable string) (*tableResult, error) {
	start := time.Now()
	folded := strings.ToLower(srcTable)
	res := &tableResult{Table: srcTable}

	srcCols, err := sourceColumns(ctx, src, srcTable)
	if err != nil {
		return nil, err
	}
	tgtCols, err := targetColumns(ctx, conn, folded)
	if err != nil {
		return nil, err
	}
	if n, err := sourceCount(ctx, src, srcTable); err != nil {
		return nil, err
	} else {
		res.Source = n
	}

	// Pair target columns (insert order) with the source column feeding
	// each; identifiers fold to lowercase on the PostgreSQL side, so the
	// match is on the folded name.
	srcByFolded := map[string]string{}
	for _, c := range srcCols {
		srcByFolded[strings.ToLower(c)] = c
	}
	type colPair struct {
		src string
		tgt pgColumn
	}
	var pairs []colPair
	usedSrc := map[string]bool{}
	for _, tc := range tgtCols {
		if s, ok := srcByFolded[tc.name]; ok {
			pairs = append(pairs, colPair{src: s, tgt: tc})
			usedSrc[s] = true
		} else {
			res.TargetOnlyColumns++
		}
	}
	for _, c := range srcCols {
		if !usedSrc[c] {
			res.SourceOnlyColumns = append(res.SourceOnlyColumns, c)
		}
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("table %s: no columns in common with the target", srcTable)
	}

	srcList := make([]string, len(pairs))
	tgtNames := make([]string, len(pairs))
	for i, p := range pairs {
		srcList[i] = `"` + p.src + `"`
		tgtNames[i] = p.tgt.name
	}
	rows, err := src.QueryContext(ctx,
		fmt.Sprintf(`SELECT %s FROM "%s"`, strings.Join(srcList, ", "), srcTable))
	if err != nil {
		return nil, fmt.Errorf("read source %s: %w", srcTable, err)
	}
	defer rows.Close()

	convertRow := func(raw []any) ([]any, *skipRecord) {
		out := make([]any, len(pairs))
		for i, p := range pairs {
			v, err := convertValue(raw[i], p.tgt)
			if err != nil {
				return nil, &skipRecord{
					RowID:  rowIdentity(raw),
					Column: p.tgt.name,
					Reason: err.Error(),
				}
			}
			out[i] = v
		}
		return out, nil
	}

	const chunkSize = 5000
	chunk := make([][]any, 0, chunkSize)
	flush := func() error {
		if len(chunk) == 0 {
			return nil
		}
		var copied int64
		var err error
		if alwaysIdentityTables[folded] {
			copied, err = insertBatch(ctx, conn, folded, tgtNames, chunk)
		} else {
			copied, err = conn.CopyFrom(ctx, pgx.Identifier{folded}, tgtNames,
				pgx.CopyFromRows(chunk))
		}
		if err != nil {
			return fmt.Errorf("write %s: %w", srcTable, err)
		}
		res.Copied += copied
		chunk = chunk[:0]
		return nil
	}

	for rows.Next() {
		raw := make([]any, len(pairs))
		holders := make([]any, len(pairs))
		for i := range raw {
			holders[i] = &raw[i]
		}
		if err := rows.Scan(holders...); err != nil {
			return nil, fmt.Errorf("scan source %s: %w", srcTable, err)
		}
		converted, skip := convertRow(raw)
		if skip != nil {
			res.Skipped++
			if len(res.Skips) < 20 {
				res.Skips = append(res.Skips, *skip)
			}
			continue
		}
		chunk = append(chunk, converted)
		if len(chunk) >= chunkSize {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate source %s: %w", srcTable, err)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	res.Duration = time.Since(start)
	return res, nil
}

// insertBatch is the write path for GENERATED ALWAYS identity tables:
// multi-row INSERT with OVERRIDING SYSTEM VALUE so migrated ids survive.
func insertBatch(ctx context.Context, conn *pgx.Conn, folded string, cols []string, batch [][]any) (int64, error) {
	const rowsPerStmt = 500
	var total int64
	for start := 0; start < len(batch); start += rowsPerStmt {
		end := start + rowsPerStmt
		if end > len(batch) {
			end = len(batch)
		}
		part := batch[start:end]
		var sb strings.Builder
		sb.WriteString("INSERT INTO ")
		sb.WriteString(pgx.Identifier{folded}.Sanitize())
		sb.WriteString(" (")
		for i, c := range cols {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(pgx.Identifier{c}.Sanitize())
		}
		sb.WriteString(") OVERRIDING SYSTEM VALUE VALUES ")
		args := make([]any, 0, len(part)*len(cols))
		for r, row := range part {
			if r > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString("(")
			for c := range cols {
				if c > 0 {
					sb.WriteString(", ")
				}
				fmt.Fprintf(&sb, "$%d", len(args)+1)
				args = append(args, row[c])
			}
			sb.WriteString(")")
		}
		tag, err := conn.Exec(ctx, sb.String(), args...)
		if err != nil {
			return total, fmt.Errorf("insert batch into %s: %w", folded, err)
		}
		total += tag.RowsAffected()
	}
	return total, nil
}

// rowIdentity renders a skipped row's first-column value for the report.
func rowIdentity(raw []any) string {
	if len(raw) == 0 || raw[0] == nil {
		return ""
	}
	s := fmt.Sprintf("%v", raw[0])
	if len(s) > 64 {
		s = s[:64] + "…"
	}
	return s
}

// convertValue converts one SQLite value (int64 / float64 / string /
// []byte / nil, as the driver returns them) into the Go value pgx
// should encode for the target column. Payload columns become jsonb per
// the schema port; everything else converts by the target's type. A
// value with no faithful representation is an error — the caller turns
// it into a skipped, reported row.
func convertValue(v any, col pgColumn) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch col.dataType {
	case "jsonb":
		raw, err := rawJSONBytes(v)
		if err != nil {
			return nil, err
		}
		if !json.Valid(raw) {
			return nil, fmt.Errorf("malformed JSON payload for jsonb column %s", col.name)
		}
		return string(raw), nil
	case "text", "character varying", "character":
		switch t := v.(type) {
		case string:
			return t, nil
		case []byte:
			return string(t), nil
		case int64:
			return strconv.FormatInt(t, 10), nil
		case float64:
			return strconv.FormatFloat(t, 'g', -1, 64), nil
		case bool:
			return strconv.FormatBool(t), nil
		case time.Time:
			return t.UTC().Format(time.RFC3339Nano), nil
		}
		return nil, fmt.Errorf("cannot render %T as text for column %s", v, col.name)
	case "bigint":
		return toInt64(v, col.name)
	case "integer", "smallint":
		n, err := toInt64(v, col.name)
		if err != nil {
			return nil, err
		}
		return int32(n), nil
	case "double precision", "real":
		switch t := v.(type) {
		case float64:
			return t, nil
		case int64:
			return float64(t), nil
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
			if err != nil {
				return nil, fmt.Errorf("value %q is not a float for column %s", t, col.name)
			}
			return f, nil
		case []byte:
			return convertValue(string(t), col)
		}
		return nil, fmt.Errorf("cannot render %T as float for column %s", v, col.name)
	case "bytea":
		switch t := v.(type) {
		case []byte:
			return t, nil
		case string:
			return []byte(t), nil
		}
		return nil, fmt.Errorf("cannot render %T as bytea for column %s", v, col.name)
	default:
		// Any other target type: pass scalars through in text form and
		// let PostgreSQL cast; report if it cannot.
		switch t := v.(type) {
		case string, int64, float64, bool, []byte:
			return t, nil
		}
		return nil, fmt.Errorf("unsupported source type %T for column %s (%s)", v, col.name, col.dataType)
	}
}

// rawJSONBytes extracts the raw payload a jsonb column should receive. A
// numeric or boolean stored value is itself a valid JSON scalar and is
// rendered as one.
func rawJSONBytes(v any) ([]byte, error) {
	switch t := v.(type) {
	case string:
		return []byte(t), nil
	case []byte:
		return t, nil
	case int64, float64, bool:
		return json.Marshal(t)
	}
	return nil, fmt.Errorf("cannot render %T as a JSON payload", v)
}

func toInt64(v any, column string) (int64, error) {
	switch t := v.(type) {
	case int64:
		return t, nil
	case float64:
		if t == math.Trunc(t) && !math.IsInf(t, 0) {
			return int64(t), nil
		}
		return 0, fmt.Errorf("value %v is not an integer for column %s", t, column)
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("value %q is not an integer for column %s", t, column)
		}
		return n, nil
	case []byte:
		return toInt64(string(t), column)
	case bool:
		if t {
			return 1, nil
		}
		return 0, nil
	}
	return 0, fmt.Errorf("cannot render %T as integer for column %s", v, column)
}
