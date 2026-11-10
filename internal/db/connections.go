package db

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"time"

	"axonrouter/proxy/internal/models"
)

// providerConnectionColumns is the single column list every connection read
// selects, so a schema addition lands in one place instead of three literals
// that have to stay in step by hand.
const providerConnectionColumns = `id, provider, authType, name, email, priority, isActive, data, lastUsedAt, consecutiveUseCount, createdAt, updatedAt`

// connectionSortOrder mirrors the JavaScript ordering: rows with no priority sort
// last, ties break on the most recently updated row.
const connectionSortOrder = `ORDER BY CASE WHEN priority IS NULL THEN 999999 ELSE priority END ASC, updatedAt DESC`

// scanConnection fills a connection from a row shaped like
// providerConnectionColumns. Both *sql.Row and *sql.Rows satisfy it, so the
// single-row and list readers cannot drift apart in column order.
func scanConnection(scan func(dest ...any) error, conn *models.ProviderConnection) error {
	return scan(
		&conn.ID, &conn.Provider, &conn.AuthType, &conn.Name, &conn.Email,
		&conn.Priority, &conn.IsActive, &conn.Data, &conn.LastUsedAt, &conn.ConsecutiveUseCount,
		&conn.CreatedAt, &conn.UpdatedAt,
	)
}

// CreateProviderConnection inserts a new provider connection carrying just an
// API key, the shape OAuth imports and CLI provisioning use.
func (r *Repo) CreateProviderConnection(id, provider, authType, name string, apiKey string) error {
	data, err := json.Marshal(map[string]string{"apiKey": apiKey})
	if err != nil {
		return fmt.Errorf("marshal provider connection data: %w", err)
	}
	now := nowUTC()
	if _, err = r.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES (?, ?, ?, ?, 1, ?, ?, ?)`,
		id, provider, authType, name, string(data), now, now,
	); err != nil {
		return fmt.Errorf("create provider connection: %w", err)
	}
	r.invalidateProviderConnections()
	return nil
}

// CreateProviderConnectionFull inserts a provider connection with an explicit
// priority and a full data payload (apiKey + providerSpecificData + testStatus
// + proxyPoolId, as the dashboard add-key modal sends them). A nil priority
// falls back to max(existing priority for the provider) + 1, matching upstream
// connectionsRepo when the caller omits priority.
func (r *Repo) CreateProviderConnectionFull(id, provider, authType, name string, priority *int, dataJSON string) error {
	if dataJSON == "" {
		dataJSON = "{}"
	}
	priorityVal := 1
	if priority != nil {
		priorityVal = *priority
	} else if next, err := r.NextConnectionPriority(provider); err == nil && next > 0 {
		priorityVal = next
	}
	now := nowUTC()
	_, err := r.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)`,
		id, provider, authType, name, priorityVal, dataJSON, now, now,
	)
	if err != nil {
		return fmt.Errorf("create provider connection: %w", err)
	}
	r.invalidateProviderConnections()
	return nil
}

// ResolveDefaultPriority returns the next sequential priority for a provider
// (max(existing priority) + 1), or 1 if no connections exist yet.
func (r *Repo) ResolveDefaultPriority(provider string) int {
	if r == nil || r.db == nil {
		return 1
	}
	if next, err := r.NextConnectionPriority(provider); err == nil && next > 0 {
		return next
	}
	return 1
}

// NextConnectionPriority returns max(priority) + 1 for a provider, or 1 when the
// provider has no connections yet.
func (r *Repo) NextConnectionPriority(provider string) (int, error) {
	var maxPriority sql.NullInt64
	if err := r.QueryRow(
		`SELECT MAX(priority) FROM providerConnections WHERE provider = ?`, provider,
	).Scan(&maxPriority); err != nil {
		return 0, fmt.Errorf("next connection priority for %s: %w", provider, err)
	}
	if !maxPriority.Valid {
		return 1, nil
	}
	return int(maxPriority.Int64) + 1, nil
}

// GetProviderConnectionByName returns the apikey connection carrying this
// (provider, authType, name), or nil when the name is free. Apikey connections
// are the ones deduped by name: oauth and access_token rows are identified by
// their account, and the user manages their duplicates by hand.
func (r *Repo) GetProviderConnectionByName(provider, authType, name string) (*models.ProviderConnection, error) {
	if name == "" {
		return nil, nil
	}
	var conn models.ProviderConnection
	err := r.QueryRow(
		`SELECT `+providerConnectionColumns+` FROM providerConnections
		 WHERE provider = ? AND authType = ? AND name = ? LIMIT 1`,
		provider, authType, name,
	).Scan(&conn.ID, &conn.Provider, &conn.AuthType, &conn.Name, &conn.Email,
		&conn.Priority, &conn.IsActive, &conn.Data, &conn.LastUsedAt, &conn.ConsecutiveUseCount,
		&conn.CreatedAt, &conn.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get provider connection by name for %s: %w", provider, err)
	}
	return &conn, nil
}

// GetProviderConnectionByID retrieves a single provider connection by primary
// key. Returns nil, nil when no row matches.
func (r *Repo) GetProviderConnectionByID(id string) (*models.ProviderConnection, error) {
	var conn models.ProviderConnection
	err := scanConnection(r.QueryRow(
		`SELECT `+providerConnectionColumns+` FROM providerConnections WHERE id = ? LIMIT 1`,
		id,
	).Scan, &conn)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &conn, nil
}

// ReplaceProviderConnectionPayload rewrites an existing connection's name and
// data in place, keeping its id and position in the rotation. This is what an
// explicit overwrite does: the caller asked to change the key behind a name
// they already own, not to add a second row with the same name.
func (r *Repo) ReplaceProviderConnectionPayload(id, name, dataJSON string) error {
	now := nowUTC()
	res, err := r.Exec(
		`UPDATE providerConnections SET name = ?, data = ?, updatedAt = ? WHERE id = ?`,
		name, dataJSON, now, id,
	)
	if err != nil {
		return fmt.Errorf("replace provider connection %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("replace provider connection %s: no such connection", id)
	}
	r.invalidateProviderConnections()
	return nil
}

// GetConnectedProviders returns the set of provider ids that have at least one
// connection row. Used to seed the auto free-tier combo with models that are
// actually reachable from the user's configured providers.
func (r *Repo) GetConnectedProviders(ctx context.Context) (map[string]struct{}, error) {
	rows, err := r.QueryContext(ctx, "SELECT DISTINCT provider FROM providerConnections")
	if err != nil {
		return nil, fmt.Errorf("get connected providers: %w", err)
	}
	defer rows.Close()

	out := make(map[string]struct{})
	for rows.Next() {
		var provider string
		if err := rows.Scan(&provider); err != nil {
			return nil, fmt.Errorf("get connected providers: scan: %w", err)
		}
		if provider != "" {
			out[provider] = struct{}{}
		}
	}
	return out, rows.Err()
}

// providerConnsKey identifies one GetProviderConnections snapshot.
type providerConnsKey struct {
	provider   string
	activeOnly bool
}

// providerConnsEntry is one cached snapshot. rows are master copies:
// callers always receive fresh clones (see cloneProviderConnections),
// so a caller mutating its slice — the selector mirrors rotation
// stamps onto its rows — can never poison the snapshot.
type providerConnsEntry struct {
	rows    []*models.ProviderConnection
	expires time.Time
	// ids lists the connection ids in rows, so storeProviderConnections
	// can retire exactly this entry's rows from the by-id index when
	// the entry is replaced or invalidated.
	ids []string
}

// providerConnsIndexEntry points at one snapshot's master row for a
// connection id, with the expiry of the snapshot it belongs to.
type providerConnsIndexEntry struct {
	row     *models.ProviderConnection
	expires time.Time
}

// providerConnsTTL bounds how long a connection snapshot may be
// served. Selection used to re-read and re-parse every connection row
// of a provider on EVERY request (review m1, feeding F1): one query
// plus a JSON hydration per row, per request, from the pool the
// completions were also fighting over. 15 seconds keeps hot provider
// selections entirely in RAM under heavy concurrency (e.g. hundreds of
// SSE requests per minute). Writers through this Repo invalidate immediately
// (see invalidateProviderConnections), and the rotation stamp writer
// patches the snapshot in place, so the in-process selection ordering
// never trails the stamps this process minted.
const providerConnsTTL = 15 * time.Second

// CachedProviderConnections returns the in-memory cached connections for a provider if fresh.
func (r *Repo) CachedProviderConnections(provider string, activeOnly bool) ([]*models.ProviderConnection, bool) {
	return r.cachedProviderConnections(providerConnsKey{provider: provider, activeOnly: activeOnly})
}

// GetCachedConnectionByID returns a connection from the in-memory cache if present and fresh,
// avoiding hitting PostgreSQL for point lookups during rotation and fallback loops.
func (r *Repo) GetCachedConnectionByID(provider, id string) (*models.ProviderConnection, bool) {
	r.providerConnsMu.RLock()
	entry, ok := r.providerConnsIndex[id]
	r.providerConnsMu.RUnlock()
	if !ok || entry.row == nil || time.Now().After(entry.expires) {
		return nil, false
	}
	if provider != "" && entry.row.Provider != provider {
		return nil, false
	}
	// A clone, as before: callers may hold and mutate the row (the
	// pinned-connection path forwards with it), and the index points
	// at the snapshot's master copy.
	return cloneProviderConnections([]*models.ProviderConnection{entry.row})[0], true
}

// cachedProviderConnections returns a clone of the snapshot for key
// when it is still fresh.
func (r *Repo) cachedProviderConnections(key providerConnsKey) ([]*models.ProviderConnection, bool) {
	r.providerConnsMu.RLock()
	entry, ok := r.providerConns[key]
	if !ok || time.Now().After(entry.expires) {
		r.providerConnsMu.RUnlock()
		return nil, false
	}
	rows := entry.rows
	r.providerConnsMu.RUnlock()

	return cloneProviderConnections(rows), true
}

// storeProviderConnections snapshots rows for key. The snapshot keeps
// its own clones: the slice being returned to the caller stays theirs.
func (r *Repo) storeProviderConnections(key providerConnsKey, rows []*models.ProviderConnection) {
	cloned := cloneProviderConnections(rows)
	expires := time.Now().Add(providerConnsTTL)
	r.providerConnsMu.Lock()
	defer r.providerConnsMu.Unlock()
	if r.providerConns == nil {
		r.providerConns = make(map[providerConnsKey]providerConnsEntry)
	}
	if r.providerConnsIndex == nil {
		r.providerConnsIndex = make(map[string]providerConnsIndexEntry)
	}
	if old, ok := r.providerConns[key]; ok {
		for _, id := range old.ids {
			delete(r.providerConnsIndex, id)
		}
	}
	ids := make([]string, 0, len(cloned))
	for _, row := range cloned {
		if row == nil || row.ID == "" {
			continue
		}
		ids = append(ids, row.ID)
		r.providerConnsIndex[row.ID] = providerConnsIndexEntry{row: row, expires: expires}
	}
	r.providerConns[key] = providerConnsEntry{
		rows:    cloned,
		expires: expires,
		ids:     ids,
	}
}

// invalidateProviderConnections drops every snapshot. Connection
// writers through this Repo call it: a dashboard edit, a lock, a
// cooldown change must be visible to the next selection immediately,
// not at the TTL boundary.
func (r *Repo) invalidateProviderConnections() {
	r.providerConnsMu.Lock()
	r.providerConns = nil
	r.providerConnsIndex = nil
	r.providerConnsMu.Unlock()
}

// patchCachedConnectionData mirrors one connection's freshly persisted
// data-blob change into every live snapshot, instead of invalidating
// them. The per-attempt state writers (model locks, rate-limit
// cooldowns, recorded errors, and their clears) call this: during a
// provider error storm those writes fire per attempt, and dropping
// every snapshot per write made the next selection re-read and
// re-hydrate the provider's whole table — tens of thousands of fat
// rows for the biggest providers — per attempt, which is what pinned
// Postgres and the app during storms. The mutation is synchronous and
// in-process, so selection freshness is identical to invalidation;
// structural writers (create, delete, disable, credential edits) keep
// full invalidation.
func (r *Repo) patchCachedConnectionData(connID string, mutate func(conn *models.ProviderConnection, raw map[string]any)) {
	r.providerConnsMu.Lock()
	defer r.providerConnsMu.Unlock()
	for _, entry := range r.providerConns {
		for _, conn := range entry.rows {
			if conn == nil || conn.ID != connID {
				continue
			}
			raw, ok := parseConnData(conn.Data)
			if !ok {
				continue
			}
			mutate(conn, raw)
			if encoded, err := json.Marshal(raw); err == nil {
				conn.Data = string(encoded)
			}
		}
	}
}

// patchCachedConnectionStamp mirrors a rotation stamp this process
// just persisted onto any snapshot holding the connection, so the
// round-robin ordering the selector computes from a snapshot stays as
// fresh as the row it would have re-read. The count is the value the
// UPDATE itself returned (RETURNING in stampConnection), not a value
// recomputed from the snapshot — so it is the persisted count even
// when another process stamped the same connection inside the
// snapshot's window (review cycle 2, N7). Cross-process stamp writes
// remain TTL-bound, exactly as documented on providerConnsTTL.
func (r *Repo) patchCachedConnectionStamp(connID, stamp string, consecutive int) {
	r.providerConnsMu.Lock()
	defer r.providerConnsMu.Unlock()
	for _, entry := range r.providerConns {
		for _, conn := range entry.rows {
			if conn.ID != connID {
				continue
			}
			stampCopy := stamp
			conn.LastUsedAt = &stampCopy
			count := consecutive
			conn.ConsecutiveUseCount = &count
		}
	}
}

// cloneProviderConnections deep-copies a connection slice, including
// the pointer fields, so neither side of the cache boundary can mutate
// the other's rows.
func cloneProviderConnections(rows []*models.ProviderConnection) []*models.ProviderConnection {
	out := make([]*models.ProviderConnection, len(rows))
	for i, row := range rows {
		if row == nil {
			continue
		}
		c := *row
		if row.Name != nil {
			v := *row.Name
			c.Name = &v
		}
		if row.Email != nil {
			v := *row.Email
			c.Email = &v
		}
		if row.Priority != nil {
			v := *row.Priority
			c.Priority = &v
		}
		if row.LastUsedAt != nil {
			v := *row.LastUsedAt
			c.LastUsedAt = &v
		}
		if row.ConsecutiveUseCount != nil {
			v := *row.ConsecutiveUseCount
			c.ConsecutiveUseCount = &v
		}
		out[i] = &c
	}
	return out
}

// GetProviderConnections retrieves provider connections. If activeOnly is true, only returns active ones.
func (r *Repo) GetProviderConnections(provider string, activeOnly bool) ([]*models.ProviderConnection, error) {
	cacheKey := providerConnsKey{provider: provider, activeOnly: activeOnly}
	if snapshot, ok := r.cachedProviderConnections(cacheKey); ok {
		return snapshot, nil
	}

	query := "SELECT " + providerConnectionColumns + " FROM providerConnections "
	var args []any

	switch {
	case provider != "" && activeOnly:
		query += "WHERE provider = ? AND isActive = 1 "
		args = append(args, provider)
	case provider != "":
		query += "WHERE provider = ? "
		args = append(args, provider)
	case activeOnly:
		query += "WHERE isActive = 1 "
	}
	query += connectionSortOrder

	// Collapse concurrent misses for the same key into one load: a
	// burst arriving just after an invalidation otherwise fires this
	// query once per waiter against the 4-connection pool. Every
	// caller clones the shared result, preserving the mutation
	// isolation the uncached path had.
	loadKey := fmt.Sprintf("%s\x1f%t", provider, activeOnly)
	loaded, err, _ := r.providerConnsLoads.Do(loadKey, func() (any, error) {
		if snapshot, ok := r.cachedProviderConnections(cacheKey); ok {
			return snapshot, nil
		}
		connections, err := r.listProviderConnections(query, args)
		if err != nil {
			return nil, err
		}
		r.storeProviderConnections(cacheKey, connections)
		return connections, nil
	})
	if err != nil {
		return nil, err
	}
	return cloneProviderConnections(loaded.([]*models.ProviderConnection)), nil
}

// GetConnectionsWithProxyPool queries only rows that have proxyPoolId set in their data JSON,
// avoiding scanning 170k rows when inspecting proxy pool bindings.
func (r *Repo) GetConnectionsWithProxyPool(ctx context.Context) ([]*models.ProviderConnection, error) {
	query := "SELECT " + providerConnectionColumns + " FROM providerConnections " +
		"WHERE (data->>'proxyPoolId') IS NOT NULL " +
		"   OR (data->'providerSpecificData'->>'proxyPoolId') IS NOT NULL"
	return r.listProviderConnections(query, nil)
}

// GetFirstConnectionPerProvider returns at most one connection per provider,
// avoiding pulling 170k duplicate rows when listing models or providers.
func (r *Repo) GetFirstConnectionPerProvider(ctx context.Context) ([]*models.ProviderConnection, error) {
	query := "SELECT DISTINCT ON (provider) " + providerConnectionColumns + " FROM providerConnections " +
		"ORDER BY provider, (CASE WHEN priority IS NULL THEN 999999 ELSE priority END) ASC, updatedAt DESC"
	return r.listProviderConnections(query, nil)
}

// GetLockedOrUnavailableConnections queries only rows that have lockedAllUntil,
// rateLimitedUntil, oauthLockedUntil, modelLocks, a flattened modelLock_* key,
// or testStatus=unavailable in their data JSON, avoiding a 170k-row scan.
//
// The flattened-key clause is load-bearing: writers store per-model locks
// almost exclusively as modelLock_<model> keys and the modelLocks object is
// routinely absent, so keying the prefilter on the object alone made the
// availability census silently skip those connections (anaking966,
// abahakunan and adenarere held live locks yet never appeared, audit
// 2026-11-09) while the Go-side reader (activeModelLocks) already reads
// both shapes.
func (r *Repo) GetLockedOrUnavailableConnections(ctx context.Context) ([]*models.ProviderConnection, error) {
	query := "SELECT " + providerConnectionColumns + " FROM providerConnections " +
		"WHERE (data->>'lockedAllUntil') IS NOT NULL " +
		"   OR (data->>'rateLimitedUntil') IS NOT NULL " +
		"   OR (data->>'oauthLockedUntil') IS NOT NULL " +
		"   OR (data->>'modelLocks') IS NOT NULL " +
		"   OR EXISTS (SELECT 1 FROM jsonb_object_keys(data) AS k WHERE k LIKE 'modelLock\\_%') " +
		"   OR EXISTS (SELECT 1 FROM jsonb_object_keys(data) AS k WHERE k LIKE 'modelGone\\_%') " +
		"   OR (data->>'testStatus') = 'unavailable' " +
		connectionSortOrder
	return r.listProviderConnections(query, nil)
}

// listProviderConnections drains a providerConnections query and closes
// its rows before returning. Callers that write per row must
// go through a drain-first helper like this one: a nested pool checkout
// while the SELECT rows are still open can deadlock the whole pool once
// four concurrent callers pile up (MaxOpenConns is 4).
func (r *Repo) listProviderConnections(query string, args []any) ([]*models.ProviderConnection, error) {
	rows, err := r.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var connections []*models.ProviderConnection
	for rows.Next() {
		var conn models.ProviderConnection
		if err := scanConnection(rows.Scan, &conn); err != nil {
			return nil, err
		}
		connections = append(connections, &conn)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return connections, nil
}

// GetActiveProviders returns the unique list of providers that have at least one active connection.
func (r *Repo) GetActiveProviders(ctx context.Context) ([]string, error) {
	query := "SELECT DISTINCT provider FROM providerConnections WHERE isActive = 1 ORDER BY provider"
	rows, err := r.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var provs []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil && p != "" {
			provs = append(provs, p)
		}
	}
	return provs, rows.Err()
}

// GetActiveOAuthConnections returns only active connections with authType = 'oauth'
// and non-empty refreshToken, avoiding pulling 150k static API keys on every background tick.
func (r *Repo) GetActiveOAuthConnections(ctx context.Context) ([]*models.ProviderConnection, error) {
	query := "SELECT " + providerConnectionColumns + " FROM providerConnections " +
		"WHERE LOWER(authType) = 'oauth' AND isActive = 1 " +
		"  AND COALESCE(data->>'status', '') NOT IN ('disabled', 'validation_required') " +
		"  AND COALESCE(data->>'testStatus', '') != 'disabled' " +
		"  AND (data->>'disabledAt') IS NULL " +
		"  AND (data->>'refreshToken') IS NOT NULL " +
		"  AND trim(data->>'refreshToken') != ''"
	return r.listProviderConnections(query, nil)
}

// GetConnectionNames returns a map of id -> displayName (name or email or id) for the specified IDs only.
func (r *Repo) GetConnectionNames(ctx context.Context, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return make(map[string]string), nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := r.Rebind("SELECT id, name, email FROM providerConnections WHERE id IN (" + strings.Join(placeholders, ",") + ")")
	rows, err := r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]string, len(ids))
	for rows.Next() {
		var id string
		var name, email sql.NullString
		if err := rows.Scan(&id, &name, &email); err == nil {
			displayName := id
			if name.Valid && strings.TrimSpace(name.String) != "" {
				displayName = name.String
			} else if email.Valid && strings.TrimSpace(email.String) != "" {
				displayName = email.String
			}
			out[id] = displayName
		}
	}
	return out, rows.Err()
}
