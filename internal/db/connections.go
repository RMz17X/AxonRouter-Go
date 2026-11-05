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
	// Seal after the insert: the sealed columns are written with one UPDATE per
	// slot, which needs the row to exist.
	sealed, err := r.sealOnWrite(id, string(data))
	if err != nil {
		return err
	}
	if sealed != string(data) {
		if _, err = r.Exec(`UPDATE providerConnections SET data = ? WHERE id = ?`, sealed, id); err != nil {
			return fmt.Errorf("create provider connection: redact data: %w", err)
		}
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
	// Seal after the insert so the credentials land in their own columns and
	// the stored payload keeps only what the model resolver must read.
	sealed, err := r.sealOnWrite(id, dataJSON)
	if err != nil {
		return err
	}
	if sealed == dataJSON {
		r.invalidateProviderConnections()
		return nil
	}
	if _, err = r.Exec(`UPDATE providerConnections SET data = ? WHERE id = ?`, sealed, id); err != nil {
		return fmt.Errorf("create provider connection: redact data: %w", err)
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
	r.hydrate(&conn)
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
	r.hydrate(&conn)
	return &conn, nil
}

// ReplaceProviderConnectionPayload rewrites an existing connection's name and
// data in place, keeping its id and position in the rotation. This is what an
// explicit overwrite does: the caller asked to change the key behind a name
// they already own, not to add a second row with the same name.
func (r *Repo) ReplaceProviderConnectionPayload(id, name, dataJSON string) error {
	// Seal first: this is the dashboard's overwrite path, so the new key must
	// never land in `data` in the clear even for the moment between the two
	// statements.
	sealed, err := r.sealOnWrite(id, dataJSON)
	if err != nil {
		return err
	}
	now := nowUTC()
	res, err := r.Exec(
		`UPDATE providerConnections SET name = ?, data = ?, updatedAt = ? WHERE id = ?`,
		name, sealed, now, id,
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

// cachedProviderConnections returns a clone of the snapshot for key
// when it is still fresh.
func (r *Repo) cachedProviderConnections(key providerConnsKey) ([]*models.ProviderConnection, bool) {
	r.providerConnsMu.Lock()
	defer r.providerConnsMu.Unlock()
	entry, ok := r.providerConns[key]
	if !ok || time.Now().After(entry.expires) {
		return nil, false
	}
	return cloneProviderConnections(entry.rows), true
}

// storeProviderConnections snapshots rows for key. The snapshot keeps
// its own clones: the slice being returned to the caller stays theirs.
func (r *Repo) storeProviderConnections(key providerConnsKey, rows []*models.ProviderConnection) {
	r.providerConnsMu.Lock()
	defer r.providerConnsMu.Unlock()
	if r.providerConns == nil {
		r.providerConns = make(map[providerConnsKey]providerConnsEntry)
	}
	r.providerConns[key] = providerConnsEntry{
		rows:    cloneProviderConnections(rows),
		expires: time.Now().Add(providerConnsTTL),
	}
}

// invalidateProviderConnections drops every snapshot. Connection
// writers through this Repo call it: a dashboard edit, a lock, a
// cooldown change must be visible to the next selection immediately,
// not at the TTL boundary.
func (r *Repo) invalidateProviderConnections() {
	r.providerConnsMu.Lock()
	r.providerConns = nil
	r.providerConnsMu.Unlock()
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

	connections, err := r.listProviderConnections(query, args)
	if err != nil {
		return nil, err
	}

	// A sealed credential is not in the data column, so each row is
	// hydrated here the way the single-row readers do it. Without this
	// the list carried a data blob with no apiKey, and every caller that
	// reads the credential from it — the chat path, the dashboard's
	// credential check, the Gemini Web rotation loop — saw an empty
	// credential while the row itself was perfectly usable.
	// Hydration runs only after listProviderConnections has closed its
	// rows: hydrate performs nested queries (sealedPairFor) that need
	// their own pool connection, and doing that while the result set is
	// still open can deadlock the pool.
	for _, conn := range connections {
		r.hydrate(conn)
	}

	r.storeProviderConnections(cacheKey, connections)
	return connections, nil
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
// modelLocks, or testStatus=unavailable in their data JSON, avoiding a 170k-row scan.
func (r *Repo) GetLockedOrUnavailableConnections(ctx context.Context) ([]*models.ProviderConnection, error) {
	query := "SELECT " + providerConnectionColumns + " FROM providerConnections " +
		"WHERE (data->>'lockedAllUntil') IS NOT NULL " +
		"   OR (data->>'modelLocks') IS NOT NULL " +
		"   OR (data->>'testStatus') = 'unavailable' " +
		connectionSortOrder
	return r.listProviderConnections(query, nil)
}

// listProviderConnections drains a providerConnections query and closes
// its rows before returning. Callers that hydrate or write per row must
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
