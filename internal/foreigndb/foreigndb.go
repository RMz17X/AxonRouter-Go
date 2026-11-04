// Package foreigndb reads rows out of database files that belong to
// other applications — a browser's cookie store, an editor's state
// file — so a credential the user already has on this machine can be
// imported instead of re-created.
//
// Those files share one paged, b-tree file format with a write-ahead
// log. This package is a small read-only reader for that format,
// written in pure Go: it exists because the gateway's own database
// driver left the module when the gateway's database became a server
// (P4), and reading a foreign file must not drag a database engine
// back into the binary for four small fixed queries. It serves
// exactly the statement shapes the import call sites issue (see
// query.go), refuses everything else, and never writes.
//
// It registers itself as the database/sql driver "foreigndb". The DSN
// is a file path, optionally in the "file:" form the call sites
// already build (any query parameters, such as mode=ro, are accepted
// and ignored: this reader is read-only by construction).
package foreigndb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
)

func init() {
	sql.Register("foreigndb", &Driver{})
}

// Driver is the database/sql driver. Stateless: every Open parses one
// file.
type Driver struct{}

// Open parses the DSN into a path and returns a connection over it.
func (d *Driver) Open(dsn string) (driver.Conn, error) {
	path := dsn
	if strings.HasPrefix(path, "file:") {
		path = path[len("file:"):]
		if q := strings.Index(path, "?"); q >= 0 {
			path = path[:q]
		}
	}
	if path == "" {
		return nil, errors.New("foreigndb: empty path")
	}
	return &conn{path: path}, nil
}

type conn struct {
	path string
}

// Prepare is unsupported beyond what QueryerContext covers: the call
// sites only ever query, and database/sql reaches QueryContext first.
func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return nil, errors.New("foreigndb: prepared statements are not supported; use QueryContext")
}

func (c *conn) Close() error { return nil }

func (c *conn) Begin() (driver.Tx, error) {
	return nil, errors.New("foreigndb: read-only; transactions are not supported")
}

// QueryContext parses and runs one SELECT against the file.
func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q, err := parseSelect(query)
	if err != nil {
		return nil, err
	}
	if q.hasWhere && len(args) != 1 {
		return nil, fmt.Errorf("foreigndb: the predicate takes exactly one argument, got %d", len(args))
	}
	if !q.hasWhere && len(args) != 0 {
		return nil, fmt.Errorf("foreigndb: statement takes no arguments, got %d", len(args))
	}

	f, err := openFile(c.path)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = f.Close()
		}
	}()

	tbl, err := f.findTable(q.table)
	if err != nil {
		return nil, err
	}
	colIdx := make(map[string]int, len(tbl.columns))
	for i, name := range tbl.columns {
		colIdx[strings.ToLower(name)] = i
	}
	resolve := func(name string) (int, error) {
		i, ok := colIdx[strings.ToLower(name)]
		if !ok {
			return 0, fmt.Errorf("foreigndb: no such column: %s", name)
		}
		return i, nil
	}

	// Expand the projection into (source index, fallback) pairs.
	type projection struct {
		idx      int
		fallback any
	}
	var proj []projection
	var outCols []string
	for _, col := range q.columns {
		if col.star {
			for i, name := range tbl.columns {
				proj = append(proj, projection{idx: i})
				outCols = append(outCols, name)
			}
			continue
		}
		i, err := resolve(col.name)
		if err != nil {
			return nil, err
		}
		p := projection{idx: i}
		if col.coalesce {
			p.fallback = col.fallback
		}
		proj = append(proj, p)
		outCols = append(outCols, col.name)
	}

	whereIdx := -1
	if q.hasWhere {
		i, err := resolve(q.whereCol)
		if err != nil {
			return nil, err
		}
		whereIdx = i
	}

	records, err := f.walkTable(tbl.rootPage, 0)
	if err != nil {
		return nil, err
	}

	r := &rows{cols: outCols}
	for _, rec := range records {
		if whereIdx >= 0 {
			var stored any
			if whereIdx < len(rec) {
				stored = rec[whereIdx]
			}
			arg := args[0].Value
			matched := false
			switch q.whereOp {
			case "=":
				matched = valueEquals(stored, arg)
			case "LIKE":
				if pat, ok := arg.(string); ok {
					matched = valueLike(stored, pat)
				}
			}
			if !matched {
				continue
			}
		}
		values := make([]driver.Value, len(proj))
		for i, p := range proj {
			var v any
			if p.idx < len(rec) {
				v = rec[p.idx]
			}
			if v == nil && p.fallback != nil {
				v = p.fallback
			}
			values[i] = v
		}
		r.data = append(r.data, values)
		if q.limit >= 0 && len(r.data) >= q.limit {
			break
		}
	}
	keep = true
	r.file = f
	return r, nil
}

// rows is an in-memory result set: the files this reader serves are
// small (a cookie store, a state file), and materialising the matched
// rows keeps the file handle's lifetime tied to the Rows, which
// database/sql closes deterministically.
type rows struct {
	cols []string
	data [][]driver.Value
	pos  int
	file *file
}

func (r *rows) Columns() []string { return r.cols }

func (r *rows) Close() error {
	if r.file != nil {
		return r.file.Close()
	}
	return nil
}

func (r *rows) Next(dest []driver.Value) error {
	if r.pos >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.pos])
	r.pos++
	return nil
}
