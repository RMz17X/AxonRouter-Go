package foreigndb

import (
	"fmt"
	"strconv"
	"strings"
)

// selectQuery is the parsed shape of the only statements this reader
// serves: a plain column list (bare names, '*', or COALESCE(name,
// literal)) from one table, with at most one single-placeholder
// predicate (equality or LIKE) and an optional LIMIT. The statements
// are the fixed ones the import call sites issue against other
// applications' files; anything outside this shape is refused rather
// than approximated.
type selectQuery struct {
	table     string
	columns   []selectColumn
	whereCol  string
	whereOp   string // "" (none), "=" or "LIKE"
	hasWhere  bool
	limit     int // -1 = no limit
}

type selectColumn struct {
	name       string // "" when star
	star       bool
	coalesce   bool
	fallback   any // literal for COALESCE: int64 or string
	hasLiteral bool
}

// parseSelect parses query into a selectQuery.
func parseSelect(query string) (*selectQuery, error) {
	q := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(query), ";"))
	upper := strings.ToUpper(q)
	if !strings.HasPrefix(upper, "SELECT ") {
		return nil, fmt.Errorf("foreigndb: only SELECT statements are served")
	}
	fromIdx := indexKeyword(upper, "FROM", len("SELECT "))
	if fromIdx < 0 {
		return nil, fmt.Errorf("foreigndb: SELECT without FROM")
	}
	colPart := strings.TrimSpace(q[len("SELECT "):fromIdx])
	rest := strings.TrimSpace(q[fromIdx+len("FROM"):])

	out := &selectQuery{limit: -1}
	// The table name is the first token of the rest.
	restFields := strings.Fields(rest)
	if len(restFields) == 0 {
		return nil, fmt.Errorf("foreigndb: SELECT without a table")
	}
	out.table = strings.Trim(restFields[0], "\"`[]")
	rest = strings.TrimSpace(rest[len(restFields[0]):])
	restUpper := strings.ToUpper(rest)

	if strings.HasPrefix(restUpper, "WHERE ") {
		cond := strings.TrimSpace(rest[len("WHERE "):])
		// An optional trailing LIMIT after the predicate is handled by
		// cutting it off the condition text first.
		if li := indexKeyword(strings.ToUpper(cond), "LIMIT", 0); li >= 0 {
			lim, err := parseLimit(cond[li:])
			if err != nil {
				return nil, err
			}
			out.limit = lim
			cond = strings.TrimSpace(cond[:li])
		}
		if err := out.parseWhere(cond); err != nil {
			return nil, err
		}
	} else if strings.HasPrefix(restUpper, "LIMIT ") {
		lim, err := parseLimit(rest)
		if err != nil {
			return nil, err
		}
		out.limit = lim
	} else if rest != "" {
		return nil, fmt.Errorf("foreigndb: unsupported SELECT tail %q", rest)
	}

	cols, err := parseColumnListSelect(colPart)
	if err != nil {
		return nil, err
	}
	out.columns = cols
	return out, nil
}

func parseLimit(text string) (int, error) {
	f := strings.Fields(text)
	if len(f) != 2 || !strings.EqualFold(f[0], "LIMIT") {
		return 0, fmt.Errorf("foreigndb: unsupported LIMIT clause %q", text)
	}
	n, err := strconv.Atoi(f[1])
	if err != nil || n < 0 {
		return 0, fmt.Errorf("foreigndb: unsupported LIMIT value %q", f[1])
	}
	return n, nil
}

// parseWhere parses "<column> = ?" or "<column> LIKE ?" — the two
// predicate shapes the call sites use.
func (q *selectQuery) parseWhere(cond string) error {
	upper := strings.ToUpper(cond)
	for _, op := range []string{" LIKE ", "="} {
		if idx := strings.Index(upper, op); idx >= 0 {
			col := strings.Trim(strings.TrimSpace(cond[:idx]), "\"`[]")
			rhs := strings.TrimSpace(cond[idx+len(op):])
			if rhs != "?" || col == "" || strings.ContainsAny(col, " \t") {
				return fmt.Errorf("foreigndb: unsupported predicate %q", cond)
			}
			q.whereCol = col
			q.whereOp = strings.TrimSpace(op)
			q.hasWhere = true
			return nil
		}
	}
	return fmt.Errorf("foreigndb: unsupported predicate %q", cond)
}

// parseColumnListSelect parses the SELECT column list: bare column
// names, '*', and COALESCE(name, literal) items.
func parseColumnListSelect(part string) ([]selectColumn, error) {
	var items []string
	depth := 0
	start := 0
	for i := 0; i < len(part); i++ {
		switch part[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				items = append(items, part[start:i])
				start = i + 1
			}
		}
	}
	items = append(items, part[start:])

	var cols []selectColumn
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "*" {
			cols = append(cols, selectColumn{star: true})
			continue
		}
		if strings.HasPrefix(strings.ToUpper(item), "COALESCE(") && strings.HasSuffix(item, ")") {
			inner := item[len("COALESCE(") : len(item)-1]
			comma := strings.Index(inner, ",")
			if comma < 0 {
				return nil, fmt.Errorf("foreigndb: unsupported COALESCE %q", item)
			}
			name := strings.Trim(strings.TrimSpace(inner[:comma]), "\"`[]")
			litText := strings.TrimSpace(inner[comma+1:])
			lit, err := parseLiteral(litText)
			if err != nil {
				return nil, err
			}
			cols = append(cols, selectColumn{name: name, coalesce: true, fallback: lit, hasLiteral: true})
			continue
		}
		if strings.ContainsAny(item, " \t()") {
			return nil, fmt.Errorf("foreigndb: unsupported select item %q", item)
		}
		cols = append(cols, selectColumn{name: strings.Trim(item, "\"`[]")})
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("foreigndb: empty select list")
	}
	return cols, nil
}

// parseLiteral parses an integer or single-quoted string literal.
func parseLiteral(text string) (any, error) {
	if len(text) >= 2 && text[0] == '\'' && text[len(text)-1] == '\'' {
		return strings.ReplaceAll(text[1:len(text)-1], "''", "'"), nil
	}
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return n, nil
	}
	return nil, fmt.Errorf("foreigndb: unsupported literal %q", text)
}

// indexKeyword finds a whole-word keyword occurrence at or after from.
func indexKeyword(upper, keyword string, from int) int {
	for i := from; i+len(keyword) <= len(upper); i++ {
		if upper[i:i+len(keyword)] != keyword {
			continue
		}
		before := byte(' ')
		if i > 0 {
			before = upper[i-1]
		}
		after := byte(' ')
		if i+len(keyword) < len(upper) {
			after = upper[i+len(keyword)]
		}
		if !isWordByte(before) && !isWordByte(after) {
			return i
		}
	}
	return -1
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

// likeMatch is the format's LIKE: '%' matches any run, '_' one
// character, ASCII case-insensitive.
func likeMatch(pattern, value string) bool {
	p := []rune(pattern)
	v := []rune(value)
	pi, vi := 0, 0
	starP, starV := -1, -1
	for vi < len(v) {
		if pi < len(p) && (p[pi] == '_' || foldRune(p[pi]) == foldRune(v[vi])) {
			pi++
			vi++
		} else if pi < len(p) && p[pi] == '%' {
			starP = pi
			starV = vi
			pi++
		} else if starP >= 0 {
			pi = starP + 1
			starV++
			vi = starV
		} else {
			return false
		}
	}
	for pi < len(p) && p[pi] == '%' {
		pi++
	}
	return pi == len(p)
}

func foldRune(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}

// valueEquals compares a stored value with a query argument for the
// equality predicate. Arguments arrive as strings from every call
// site; a numeric column compares numerically when the argument
// parses as a number.
func valueEquals(stored any, arg any) bool {
	switch s := stored.(type) {
	case nil:
		return arg == nil
	case string:
		a, ok := arg.(string)
		return ok && s == a
	case []byte:
		a, ok := arg.(string)
		return ok && string(s) == a
	case int64:
		switch a := arg.(type) {
		case int64:
			return s == a
		case string:
			n, err := strconv.ParseInt(a, 10, 64)
			return err == nil && s == n
		}
	case float64:
		if a, ok := arg.(string); ok {
			f, err := strconv.ParseFloat(a, 64)
			return err == nil && s == f
		}
	}
	return false
}

// valueLike renders a stored value for the LIKE predicate.
func valueLike(stored any, pattern string) bool {
	var text string
	switch s := stored.(type) {
	case string:
		text = s
	case []byte:
		text = string(s)
	case int64:
		text = strconv.FormatInt(s, 10)
	default:
		return false
	}
	return likeMatch(pattern, text)
}
