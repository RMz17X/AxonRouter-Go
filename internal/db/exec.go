package db

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"sync"
)

// exec.go is step 0 of the PostgreSQL dialect sweep (P1): how a statement
// written once — in the SQLite-flavoured form this codebase already speaks,
// '?' placeholders — reaches a PostgreSQL handle unchanged in meaning.
//
// The mechanism is deliberately small:
//
//   - rebindQuery rewrites '?' placeholders to $1..$n for the PostgreSQL
//     dialect and is the identity for SQLite, so SQLite behaviour is
//     unchanged byte for byte. The scan is quote- and comment-aware: a '?'
//     inside a string literal or a comment is never renumbered.
//   - The Repo wrappers below (Exec/Query/QueryRow and their Context
//     forms) rebind and delegate to the handle, so a call site migrates by
//     dropping its `.db` — nothing else about the statement changes.
//   - Repo.Rebind exposes the rewrite for the call sites that cannot use a
//     wrapper: statements on a *sql.Tx a Repo method opened (the
//     transaction does not know its dialect; the Repo does), and
//     package-level helpers handed the Repo.
//
// Statements routed through here must be authored in '?' form. SQL that is
// already PostgreSQL-native (schema_pg.go) keeps using the raw handle, as
// do SQLite-only paths the PG bootstrap never runs (schema.go,
// BackfillCompressionAnalytics).
//
// A cluster adopts the mechanism file by file: route the file's statements
// through the wrappers, then add dialect branches only where the SQL
// itself differs — the JSON expressions in jsonexpr.go, upsert forms, and
// PRAGMA-class maintenance, which has no PG form and is gated off instead.

// rebindCache memoises rebindQuery's PostgreSQL output keyed by the
// source statement (Efficiency Mandate, 2026-11-04: the PG layer must
// not rescan SQL text on every execution in steady state). The corpus
// of statements is fixed at build time — every query string in this
// package is a source literal or a deterministic assembly of them — so
// the working set is a few hundred entries and the cache reaches a
// steady state where every execution is one map read. The cap is a
// leak guard, not a sizing decision: past it the map resets and
// rewarms, which costs one scan per distinct statement, never a wrong
// result.
const rebindCacheMax = 4096

var (
	rebindCacheMu sync.Mutex
	rebindCache   = make(map[string]string, 512)
)

// rebindQuery rewrites the '?' placeholders of a statement to PostgreSQL's
// $1..$n for the PostgreSQL dialect. Any other dialect gets the statement
// back unchanged.
func rebindQuery(d Dialect, query string) string {
	if d != DialectPostgres || !strings.Contains(query, "?") {
		return query
	}
	rebindCacheMu.Lock()
	cached, ok := rebindCache[query]
	rebindCacheMu.Unlock()
	if ok {
		return cached
	}
	rebound := rebindScan(query)
	rebindCacheMu.Lock()
	if len(rebindCache) >= rebindCacheMax {
		rebindCache = make(map[string]string, 512)
	}
	rebindCache[query] = rebound
	rebindCacheMu.Unlock()
	return rebound
}

// rebindScan is the quote- and comment-aware rewrite rebindQuery caches.
func rebindScan(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 16)
	n := 0
	i := 0
	for i < len(query) {
		c := query[i]
		switch c {
		case '\'':
			// String literal: copy through the closing quote; '' is an
			// escaped quote inside the literal.
			j := i + 1
			for j < len(query) {
				if query[j] == '\'' {
					if j+1 < len(query) && query[j+1] == '\'' {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			b.WriteString(query[i:j])
			i = j
		case '"', '`':
			// Quoted identifier: copy through the matching quote.
			j := i + 1
			for j < len(query) && query[j] != c {
				j++
			}
			if j < len(query) {
				j++
			}
			b.WriteString(query[i:j])
			i = j
		case '-':
			if i+1 < len(query) && query[i+1] == '-' {
				// Line comment: copy to the newline.
				j := i
				for j < len(query) && query[j] != '\n' {
					j++
				}
				b.WriteString(query[i:j])
				i = j
			} else {
				b.WriteByte(c)
				i++
			}
		case '/':
			if i+1 < len(query) && query[i+1] == '*' {
				// Block comment: copy through the closer.
				end := strings.Index(query[i+2:], "*/")
				if end < 0 {
					b.WriteString(query[i:])
					i = len(query)
				} else {
					j := i + 2 + end + 2
					b.WriteString(query[i:j])
					i = j
				}
			} else {
				b.WriteByte(c)
				i++
			}
		case '?':
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// Rebind rewrites a statement's placeholders for the Repo's dialect. It is
// the escape hatch for statements executed on something the wrappers do
// not cover — a *sql.Tx opened by a Repo method, or a *sql.DB a package
// helper was handed alongside the Repo.
func (r *Repo) Rebind(query string) string {
	return rebindQuery(r.dialect, query)
}

// ExecContext executes a statement through the Repo's handle with its
// placeholders rebound for the handle's dialect.
func (r *Repo) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return r.db.ExecContext(ctx, r.Rebind(query), args...)
}

// Exec is ExecContext with the background context, for the call sites that
// predate context plumbing.
func (r *Repo) Exec(query string, args ...any) (sql.Result, error) {
	return r.db.Exec(r.Rebind(query), args...)
}

// QueryContext runs a query through the Repo's handle with its
// placeholders rebound for the handle's dialect.
func (r *Repo) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return r.db.QueryContext(ctx, r.Rebind(query), args...)
}

// Query is QueryContext with the background context.
func (r *Repo) Query(query string, args ...any) (*sql.Rows, error) {
	return r.db.Query(r.Rebind(query), args...)
}

// QueryRowContext runs a one-row query through the Repo's handle with its
// placeholders rebound for the handle's dialect.
func (r *Repo) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return r.db.QueryRowContext(ctx, r.Rebind(query), args...)
}

// QueryRow is QueryRowContext with the background context.
func (r *Repo) QueryRow(query string, args ...any) *sql.Row {
	return r.db.QueryRow(r.Rebind(query), args...)
}
