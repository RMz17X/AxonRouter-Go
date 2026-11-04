package db

import (
	"strconv"
	"strings"
)

// jsonexpr.go is the dialect sweep's JSON toolkit (P1): the expression
// builders for reaching into a JSON payload column, per dialect.
//
// The columns these expressions read are JSON *text* on SQLite and *jsonb*
// on PostgreSQL (schema_pg_defs.go names the set: requestDetails.data and
// usageHistory.tokens are the two SQL itself extracts from). That storage
// difference drives every mapping below:
//
//   - json_valid(col) exists to guard the extraction against malformed
//     legacy text. A jsonb column cannot hold malformed JSON, so on
//     PostgreSQL the guard collapses to a presence check — jsonValid
//     renders it; call sites keep stating the guard rather than deleting
//     it, so the dual-dialect period reads the same in both dialects.
//   - SQLite's json_extract returns the JSON value (a number stays a
//     number). PostgreSQL's #>> returns text, so the numeric forms cast —
//     and the integer form truncates through numeric, mirroring CAST(x AS
//     INTEGER) on SQLite, because token counts are integers by
//     construction and a fractional payload must fold the same way on
//     both backends.
//   - A JSON null and a missing key both read as SQL NULL in both
//     dialects (#>> yields SQL NULL for a stored JSON null, exactly like
//     json_extract), so COALESCE chains port unchanged around these.
//
// Path segments are passed as strings; an all-digits segment is an array
// index in both renderings ('$.a[0].b' on SQLite, {a,0,b} on PostgreSQL).

// jsonPath renders a path for SQLite's json_extract: '$.a.b', '$.a[0].b'.
func jsonPath(path []string) string {
	var b strings.Builder
	b.WriteString("$")
	for _, seg := range path {
		if isIndexSegment(seg) {
			b.WriteString("[")
			b.WriteString(seg)
			b.WriteString("]")
		} else {
			b.WriteString(".")
			b.WriteString(seg)
		}
	}
	return b.String()
}

// jsonPathPG renders a path for PostgreSQL's #> / #>> operators: {a,b},
// {a,0,b}.
func jsonPathPG(path []string) string {
	return "{" + strings.Join(path, ",") + "}"
}

func isIndexSegment(seg string) bool {
	if seg == "" {
		return false
	}
	_, err := strconv.Atoi(seg)
	return err == nil
}

// jsonTextAt is the scalar at path as text — a stored string unquoted, a
// stored number in its text form — or NULL when the path is absent.
func jsonTextAt(d Dialect, column string, path ...string) string {
	if d == DialectPostgres {
		return "(" + column + " #>> '" + jsonPathPG(path) + "')"
	}
	return "json_extract(" + column + ", '" + jsonPath(path) + "')"
}

// jsonNumAt is the scalar at path as a number, or NULL when absent. On
// SQLite this is json_extract itself (it returns the JSON number); on
// PostgreSQL the text form is cast to numeric.
func jsonNumAt(d Dialect, column string, path ...string) string {
	if d == DialectPostgres {
		return "(" + jsonTextAt(d, column, path...) + "::numeric)"
	}
	return "json_extract(" + column + ", '" + jsonPath(path) + "')"
}

// jsonIntAt is the scalar at path as an integer, with SQLite's
// CAST(x AS INTEGER) truncation semantics, or NULL when absent.
func jsonIntAt(d Dialect, column string, path ...string) string {
	if d == DialectPostgres {
		return "(trunc(" + jsonTextAt(d, column, path...) + "::numeric)::bigint)"
	}
	return "CAST(json_extract(" + column + ", '" + jsonPath(path) + "') AS INTEGER)"
}

// jsonRealAt is the scalar at path as a real number, or NULL when absent.
func jsonRealAt(d Dialect, column string, path ...string) string {
	if d == DialectPostgres {
		return "(" + jsonTextAt(d, column, path...) + "::float8)"
	}
	return "CAST(json_extract(" + column + ", '" + jsonPath(path) + "') AS REAL)"
}

// jsonDocAt is the sub-document at path as JSON text — what a reader that
// scans the payload into a Go string consumes. SQLite returns the stored
// text of the sub-document; PostgreSQL returns jsonb's canonical rendering
// of it (key order and spacing are jsonb's, the document is the same).
func jsonDocAt(d Dialect, column string, path ...string) string {
	if d == DialectPostgres {
		return "(" + column + " #> '" + jsonPathPG(path) + "')"
	}
	return "json_extract(" + column + ", '" + jsonPath(path) + "')"
}

// jsonValid renders the "this column holds usable JSON" guard for the
// dialect: json_valid on SQLite, a presence check on PostgreSQL, where a
// jsonb column is valid by construction.
func jsonValid(d Dialect, column string) string {
	if d == DialectPostgres {
		return column + " IS NOT NULL"
	}
	return "json_valid(" + column + ")"
}
