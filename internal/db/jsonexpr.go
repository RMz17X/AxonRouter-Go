package db

import (
	"strconv"
	"strings"
)

// jsonexpr.go is the JSON toolkit: the expression builders for reaching
// into a JSON payload column.
//
// The columns these expressions read are jsonb (schema_pg_defs.go names
// the set: requestDetails.data and usageHistory.tokens are the two SQL
// itself extracts from). That storage fact drives every rendering below:
//
//   - json_valid existed, in the file-backend era, to guard extraction
//     against malformed legacy text. A jsonb column cannot hold
//     malformed JSON, so the guard collapses to a presence check —
//     jsonValid renders it; call sites keep stating the guard so the
//     intent stays visible at the statement.
//   - The #>> operator returns text, so the numeric forms cast — and
//     the integer form truncates through numeric, mirroring
//     CAST(x AS INTEGER) semantics, because token counts are integers
//     by construction and a fractional payload must fold the same way.
//   - A JSON null and a missing key both read as SQL NULL (#>> yields
//     SQL NULL for a stored JSON null), so COALESCE chains wrap these
//     expressions unchanged.
//
// Path segments are passed as strings; an all-digits segment is an
// array index ({a,0,b}).

// jsonPath renders a path for PostgreSQL's #> / #>> operators: {a,b},
// {a,0,b}.
func jsonPath(path []string) string {
	return "{" + strings.Join(path, ",") + "}"
}

// jsonTextAt is the scalar at path as text — a stored string unquoted, a
// stored number in its text form — or NULL when the path is absent.
func jsonTextAt(column string, path ...string) string {
	return "(" + column + " #>> '" + jsonPath(path) + "')"
}

// jsonNumAt is the scalar at path as a number, or NULL when absent: the
// text form cast to numeric.
func jsonNumAt(column string, path ...string) string {
	return "(" + jsonTextAt(column, path...) + "::numeric)"
}

// jsonIntAt is the scalar at path as an integer, with CAST(x AS INTEGER)
// truncation semantics, or NULL when absent.
func jsonIntAt(column string, path ...string) string {
	return "(trunc(" + jsonTextAt(column, path...) + "::numeric)::bigint)"
}

// jsonRealAt is the scalar at path as a real number, or NULL when absent.
func jsonRealAt(column string, path ...string) string {
	return "(" + jsonTextAt(column, path...) + "::float8)"
}

// jsonDocAt is the sub-document at path as JSON text — what a reader
// that scans the payload into a Go string consumes. jsonb returns its
// canonical rendering of the sub-document (key order and spacing are
// jsonb's, the document is the same).
func jsonDocAt(column string, path ...string) string {
	return "(" + column + " #> '" + jsonPath(path) + "')"
}

// jsonValid renders the "this column holds usable JSON" guard: a
// presence check, because a jsonb column is valid by construction.
func jsonValid(column string) string {
	return column + " IS NOT NULL"
}

// ── jsonb_set ────────────────────────────────────────────────────────
//
// PostgreSQL's jsonb_set takes one literal path array per call, so a
// multi-pair update becomes a chain of jsonb_set calls nested around
// the column. The vocabulary below renders that chain from one pair
// list, so a call site states its pairs once.
//
// The value argument is where the care goes, because the same Go
// argument is a different SQL expression per kind:
//
//   - a bound scalar (a Go string or int) is stored as the JSON scalar;
//     jsonb_set wants a jsonb, so the rendering wraps the placeholder
//     in to_jsonb with the cast that pins the parameter's type —
//     to_jsonb(?::text) for a string, to_jsonb(?::bigint) for an int.
//     The cast is load-bearing: an untyped placeholder leaves the
//     polymorphic to_jsonb unresolvable (SQLSTATE 42804).
//   - a bound parameter holding JSON text is spelled ?::jsonb.
//   - a JSON null is the explicit 'null'::jsonb, because a SQL NULL
//     argument would null the whole document instead of the key.
//   - a literal in the statement text ('error', 0) becomes
//     to_jsonb('error') / to_jsonb(0).

// jsonSetValue is one value argument of a jsonb_set-style update,
// rendered by the constructors below.
type jsonSetValue string

// jsonStringParam is a bound Go string stored as a JSON string.
func jsonStringParam() jsonSetValue { return "to_jsonb(?::text)" }

// jsonIntParam is a bound Go int stored as a JSON number.
func jsonIntParam() jsonSetValue { return "to_jsonb(?::bigint)" }

// jsonDocParam is a bound parameter whose text is already a JSON
// document (the lastError payloads).
func jsonDocParam() jsonSetValue { return "?::jsonb" }

// jsonNullLiteral is the 'null'::jsonb spelling of a JSON null value.
func jsonNullLiteral() jsonSetValue { return "'null'::jsonb" }

// jsonNullSQLValue is a SQL NULL value argument, which the chain stores
// as a JSON null.
func jsonNullSQLValue() jsonSetValue { return "'null'::jsonb" }

// jsonStringLiteral is a string literal in the statement text stored as
// a JSON string. The literal is cast to text: a bare literal is
// type-unknown and the polymorphic to_jsonb refuses it (SQLSTATE
// 42804), the same reason the parameter forms carry casts.
func jsonStringLiteral(s string) jsonSetValue {
	return jsonSetValue("to_jsonb('" + s + "'::text)")
}

// jsonIntLiteral is an integer literal in the statement text stored as
// a JSON number.
func jsonIntLiteral(n int) jsonSetValue {
	return jsonSetValue("to_jsonb(" + strconv.Itoa(n) + "::bigint)")
}

// jsonSetPair is one path/value pair of a jsonb_set-style update. The
// path is a literal path array; a bound path renders as a one-element
// path array (the caller binds the bare key).
type jsonSetPair struct {
	path  string
	value jsonSetValue
}

// jsonSetPairAt pairs a value with a static path given as segments.
func jsonSetPairAt(value jsonSetValue, path ...string) jsonSetPair {
	return jsonSetPair{
		path:  "'" + jsonPath(path) + "'",
		value: value,
	}
}

// jsonSetBoundPair pairs a value with a key the caller binds — the
// modelLock_<model> keys, whose name is data, not code. The caller
// binds the bare key.
func jsonSetBoundPair(value jsonSetValue) jsonSetPair {
	return jsonSetPair{path: "ARRAY[?]", value: value}
}

// jsonSetExpr renders a jsonb_set-chain update of column from its
// pairs. Pairs apply left to right.
func jsonSetExpr(column string, pairs ...jsonSetPair) string {
	expr := column
	for _, p := range pairs {
		expr = "jsonb_set(" + expr + ", " + p.path + ", " + string(p.value) + ")"
	}
	return expr
}
