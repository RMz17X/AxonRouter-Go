package db

import (
	"context"
	"fmt"
	"strings"

	"axonrouter/proxy/internal/models"
)

// ClientUsagePageFilter carries one /api/providers/client list request
// (the quota tracker's data source). Every filter the handler used to
// apply in memory over a full-table load is expressed here in SQL, so
// a page fetch reads the counts and the page slice only — searching
// for an account on page 40 no longer depends on what is loaded.
type ClientUsagePageFilter struct {
	Provider      string   // "" = all providers
	AccountStatus string   // "all" | "active" | "inactive"
	Search        string   // raw search text; "" = no search filter
	Supported     []string // usage-supported providers (the eligibility list)
	APIKey        []string // providers also eligible with an API key
	Page          int
	PageSize      int
}

// ClientUsagePage is one filtered page plus the counts at each filter
// level, matching the response the handler has always produced.
type ClientUsagePage struct {
	Connections           []*models.ProviderConnection
	Total                 int // after every filter, search included
	EligibleTotal         int // eligibility only
	ProviderFilteredTotal int // eligibility + provider
	ProviderOptions       []string
	Page                  int // the clamped page actually served
	TotalPages            int
}

// FatalDisableErrorSignatures are the stored-error substrings that mark
// an account as fatally disabled. The dashboard's auto-disable sweep
// (isFatalAccountDisableError) matches on this same list; the SQL
// pre-filter in GetFatalDisableCandidates must stay a superset of the
// Go check, which re-verifies every candidate exactly.
var FatalDisableErrorSignatures = []string{
	"disabled in this account",
	"terms of service",
	"tos_violation",
	"account disabled",
	"validation_required",
	"verify your account",
	"email_verification_required",
	"verify your email address",
	"invalid_grant",
	"token refresh rejected",
}

// inClause appends one ? placeholder per value and returns the column's
// IN (...) predicate.
func providerInClause(column string, values []string, args *[]any) string {
	ph := make([]string, len(values))
	for i, v := range values {
		ph[i] = "?"
		*args = append(*args, v)
	}
	return column + " IN (" + strings.Join(ph, ",") + ")"
}

// clientUsageWhere builds the WHERE clause for one filter level:
// 0 = eligibility, 1 = + provider, 2 = + account status and search.
// The predicates translate the handler's previous in-memory filters
// one for one:
//   - eligibility: provider is usage-supported AND (oauth OR an
//     api-key provider) — isUsageEligibleConnection.
//   - account status: IsEffectivelyActive(isActive, "", cooldown)
//     reduces, for an empty testStatus and lastError, to isActive = 1
//     with no live modelLock_* entry in data — HasActiveCooldown's raw
//     substring check over the JSON text.
//   - search: literal substring (LIKE-escaped) over the identity
//     fields the page displays, mirroring upstream's SQL search set.
func clientUsageWhere(f ClientUsagePageFilter, level int) (string, []any) {
	var clauses []string
	var args []any
	clauses = append(clauses, "("+providerInClause("provider", f.Supported, &args)+
		" AND (authType = 'oauth' OR "+providerInClause("provider", f.APIKey, &args)+"))")
	if level >= 1 && f.Provider != "" {
		clauses = append(clauses, "provider = ?")
		args = append(args, f.Provider)
	}
	if level >= 2 {
		active := "(isActive = 1 AND strpos(COALESCE(data::text, ''), '\"modelLock_') = 0)"
		switch f.AccountStatus {
		case "active":
			clauses = append(clauses, active)
		case "inactive":
			clauses = append(clauses, "NOT "+active)
		}
		if strings.TrimSpace(f.Search) != "" {
			clauses = append(clauses, clientUsageSearchClause(&args, f.Search))
		}
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// clientUsageSearchClause matches the search text as a literal,
// case-insensitive substring across the account identity fields:
// name, email, connection id, provider, and the display identity
// stored in data (displayName, username, githubLogin) — the label the
// page renders is name, then email, then displayName, so all three
// must be findable.
func clientUsageSearchClause(args *[]any, search string) string {
	pattern := "%" + escapeLikePattern(strings.TrimSpace(search)) + "%"
	fields := []string{
		"name", "email", "id", "provider",
		"data->>'displayName'", "data->>'username'", "data->>'githubLogin'",
	}
	parts := make([]string, len(fields))
	for i, field := range fields {
		parts[i] = field + " ILIKE ? ESCAPE '\\'"
		*args = append(*args, pattern)
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// escapeLikePattern quotes the LIKE wildcards so the search text keeps
// the literal-substring semantics the previous in-memory matcher had.
func escapeLikePattern(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "%", "\\%")
	s = strings.ReplaceAll(s, "_", "\\_")
	return s
}

// providerOrderCase renders the ORDER BY rank expression that groups
// providers in eligibility-list order, replicating
// sortProviderConnections: list order first (unlisted last), then
// provider name, then priority (nil = 0), then name. The trailing id
// tiebreak keeps page boundaries stable for exact ties, which the
// stable in-memory sort got from the underlying row order.
func providerOrderCase(providers []string, args *[]any) string {
	var sb strings.Builder
	sb.WriteString("CASE provider")
	for i, p := range providers {
		fmt.Fprintf(&sb, " WHEN ? THEN %d", i)
		*args = append(*args, p)
	}
	fmt.Fprintf(&sb, " ELSE %d END", len(providers))
	return sb.String()
}

func (r *Repo) countClientUsage(ctx context.Context, f ClientUsagePageFilter, level int) (int, error) {
	where, args := clientUsageWhere(f, level)
	var n int
	err := r.QueryRowContext(ctx, "SELECT COUNT(*) FROM providerConnections"+where, args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("Repo.countClientUsage: %w", err)
	}
	return n, nil
}

// GetClientUsagePage serves one /api/providers/client page entirely
// from SQL: three counts (eligibility, + provider, + status/search),
// the provider option list when unfiltered, and the page slice in the
// handler's established order.
func (r *Repo) GetClientUsagePage(ctx context.Context, f ClientUsagePageFilter) (*ClientUsagePage, error) {
	out := &ClientUsagePage{}
	var err error
	if out.EligibleTotal, err = r.countClientUsage(ctx, f, 0); err != nil {
		return nil, err
	}
	if out.ProviderFilteredTotal, err = r.countClientUsage(ctx, f, 1); err != nil {
		return nil, err
	}
	if out.Total, err = r.countClientUsage(ctx, f, 2); err != nil {
		return nil, err
	}

	if f.Provider == "" {
		out.ProviderOptions = []string{}
		where, args := clientUsageWhere(f, 0)
		rows, err := r.QueryContext(ctx, "SELECT DISTINCT provider FROM providerConnections"+where+" ORDER BY provider", args...)
		if err != nil {
			return nil, fmt.Errorf("Repo.GetClientUsagePage options: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err == nil {
				out.ProviderOptions = append(out.ProviderOptions, p)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("Repo.GetClientUsagePage options: %w", err)
		}
	}

	out.TotalPages = 1
	if out.Total > 0 {
		out.TotalPages = (out.Total + f.PageSize - 1) / f.PageSize
	}
	out.Page = f.Page
	if out.Page > out.TotalPages {
		out.Page = out.TotalPages
	}
	offset := (out.Page - 1) * f.PageSize

	where, args := clientUsageWhere(f, 2)
	order := providerOrderCase(f.Supported, &args)
	query := "SELECT " + providerConnectionColumns + " FROM providerConnections" + where +
		" ORDER BY " + order + ", provider, COALESCE(priority, 0), COALESCE(name, ''), id LIMIT ? OFFSET ?"
	args = append(args, f.PageSize, offset)
	conns, err := r.listProviderConnectionsContext(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("Repo.GetClientUsagePage rows: %w", err)
	}
	out.Connections = conns
	return out, nil
}

// GetFatalDisableCandidates returns the still-active connections whose
// stored lastError carries a fatal-disable signature — the only rows
// the dashboard's auto-disable sweep can act on. The SQL match is a
// pre-filter (data->>'lastError' flattens both stored shapes into
// text); the sweep re-checks each row with the exact Go predicate.
func (r *Repo) GetFatalDisableCandidates(ctx context.Context) ([]*models.ProviderConnection, error) {
	parts := make([]string, len(FatalDisableErrorSignatures))
	args := make([]any, len(FatalDisableErrorSignatures))
	for i, sig := range FatalDisableErrorSignatures {
		parts[i] = "strpos(lower(COALESCE(data->>'lastError', '')), ?) > 0"
		args[i] = sig
	}
	query := "SELECT " + providerConnectionColumns + " FROM providerConnections WHERE isActive = 1 AND (" +
		strings.Join(parts, " OR ") + ")"
	conns, err := r.listProviderConnectionsContext(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("Repo.GetFatalDisableCandidates: %w", err)
	}
	return conns, nil
}
