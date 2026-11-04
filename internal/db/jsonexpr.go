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

// ── json_set ─────────────────────────────────────────────────────────────
//
// SQLite's json_set takes variadic path/value pairs in one call, with the
// path itself allowed to be a bound parameter. PostgreSQL's jsonb_set
// takes one literal path array per call, so a multi-pair update becomes
// a chain of jsonb_set calls nested around the column. The vocabulary
// below renders both from one pair list, so a call site states its pairs
// once; the SQLite rendering is the json_set text the code has always
// run (modulo whitespace), the PostgreSQL rendering is its chain.
//
// The value argument is where the dialects really differ, because the
// same Go argument is a different SQL expression in each:
//
//   - a bound scalar (a Go string or int) is stored by json_set as the
//     JSON scalar directly; jsonb_set wants a jsonb, so the PostgreSQL
//     arm wraps the placeholder in to_jsonb with the cast that pins the
//     parameter's type — to_jsonb(?::text) for a string, to_jsonb(?::bigint)
//     for an int. The cast is load-bearing: an untyped placeholder leaves
//     the polymorphic to_jsonb unresolvable (SQLSTATE 42804).
//   - a bound parameter holding JSON text is spelled json(?) on SQLite
//     and ?::jsonb on PostgreSQL.
//   - a JSON null is json('null') or a bare SQL NULL on SQLite (both set
//     the key to JSON null there); jsonb_set wants the explicit
//     'null'::jsonb, because a SQL NULL argument would null the whole
//     document instead of the key.
//   - a literal in the statement text ('error', 0) stays a literal on
//     SQLite and becomes to_jsonb('error') / to_jsonb(0) on PostgreSQL.

// jsonSetValue is one value argument of a json_set-style update, rendered
// per dialect by the constructors below.
type jsonSetValue struct {
	sqlite string
	pg     string
}

// jsonStringParam is a bound Go string stored as a JSON string.
func jsonStringParam() jsonSetValue { return jsonSetValue{"?", "to_jsonb(?::text)"} }

// jsonIntParam is a bound Go int stored as a JSON number.
func jsonIntParam() jsonSetValue { return jsonSetValue{"?", "to_jsonb(?::bigint)"} }

// jsonDocParam is a bound parameter whose text is already a JSON
// document (the lastError payloads).
func jsonDocParam() jsonSetValue { return jsonSetValue{"json(?)", "?::jsonb"} }

// jsonNullLiteral is the json('null') spelling of a JSON null value.
func jsonNullLiteral() jsonSetValue { return jsonSetValue{"json('null')", "'null'::jsonb"} }

// jsonNullSQLValue is a bare SQL NULL value argument, which SQLite's
// json_set stores as a JSON null.
func jsonNullSQLValue() jsonSetValue { return jsonSetValue{"NULL", "'null'::jsonb"} }

// jsonStringLiteral is a string literal in the statement text stored as
// a JSON string. The PostgreSQL arm casts the literal to text: a bare
// literal is type-unknown there and the polymorphic to_jsonb refuses it
// (SQLSTATE 42804), the same reason the parameter forms carry casts.
func jsonStringLiteral(s string) jsonSetValue {
	return jsonSetValue{"'" + s + "'", "to_jsonb('" + s + "'::text)"}
}

// jsonIntLiteral is an integer literal in the statement text stored as
// a JSON number.
func jsonIntLiteral(n int) jsonSetValue {
	return jsonSetValue{strconv.Itoa(n), "to_jsonb(" + strconv.Itoa(n) + "::bigint)"}
}

// jsonSetPair is one path/value pair of a json_set-style update. The path
// is rendered per dialect; a bound path renders as the bare placeholder
// on SQLite (the caller binds the full path string) and as a one-element
// path array on PostgreSQL (the caller binds the bare key).
type jsonSetPair struct {
	sqlitePath string
	pgPath     string
	value      jsonSetValue
}

// jsonSetPairAt pairs a value with a static path given as segments.
func jsonSetPairAt(value jsonSetValue, path ...string) jsonSetPair {
	return jsonSetPair{
		sqlitePath: "'" + jsonPath(path) + "'",
		pgPath:     "'" + jsonPathPG(path) + "'",
		value:      value,
	}
}

// jsonSetBoundPair pairs a value with a key the caller binds — the
// modelLock_<model> keys, whose name is data, not code. The argument the
// caller passes differs by dialect in step with the rendering: the full
// SQLite path string on one arm, the bare key on the other.
func jsonSetBoundPair(value jsonSetValue) jsonSetPair {
	return jsonSetPair{sqlitePath: "?", pgPath: "ARRAY[?]", value: value}
}

// jsonSetExpr renders a json_set-style update of column from its pairs:
// one json_set call on SQLite, a jsonb_set chain on PostgreSQL. Pairs
// apply left to right in both renderings.
func jsonSetExpr(d Dialect, column string, pairs ...jsonSetPair) string {
	if d == DialectPostgres {
		expr := column
		for _, p := range pairs {
			expr = "jsonb_set(" + expr + ", " + p.pgPath + ", " + p.value.pg + ")"
		}
		return expr
	}
	var b strings.Builder
	b.WriteString("json_set(" + column)
	for _, p := range pairs {
		b.WriteString(", " + p.sqlitePath + ", " + p.value.sqlite)
	}
	b.WriteString(")")
	return b.String()
}
