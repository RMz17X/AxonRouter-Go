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

const modelLockPrefix = "modelLock_"

// modelGonePrefix marks the durable half of a banned-class model lock
// (upstream says the model does not exist for this account: a 404
// NOT_FOUND and its sibling sentences). A gone lock is written alongside
// its modelLock_ twin and read by the same checks, but the bulk quota
// clear (ClearConnectionModelLocks, driven by the usage auto-release)
// must never resurrect a model the upstream has disowned — a healthy
// quota read for the account's OTHER models used to wipe fresh 404
// locks within minutes, so the fleet retried a gone model hundreds of
// times per account. Gone keys clear only by expiry, by a proven
// success on the pair (ClearConnectionSuccessState), or by an explicit
// operator reset (ResetConnectionHealthState, UnlockConnectionModel,
// ResetProviderHealth).
const modelGonePrefix = "modelGone_"

// isModelLockKey reports whether a data-blob key carries per-model lock
// state of either family (transient modelLock_ or durable modelGone_).
func isModelLockKey(k string) bool {
	return strings.HasPrefix(k, modelLockPrefix) || strings.HasPrefix(k, modelGonePrefix)
}

// SetConnectionProjectID stores a discovered provider project id in the
// providerConnections.data JSON blob using a json_set-style update. It is
// the Repo-side form of the statement handlers/chat's Gemini path used to
// spell against the raw handle (the json_set vocabulary is this package's,
// so the statement lives here like the other connection-data updaters).
func (r *Repo) SetConnectionProjectID(connID, projectID string) error {
	_, err := r.Exec(
		`UPDATE providerConnections SET data = `+jsonSetExpr("data",
			jsonSetPairAt(jsonStringParam(), "projectId"),
		)+` WHERE id = ?`,
		projectID, connID,
	)
	if err != nil {
		return fmt.Errorf("set connection project id %s: %w", connID, err)
	}
	r.invalidateProviderConnections()
	return nil
}

// SetConnectionAntigravityIdentity persists a connection's pinned agy
// identity snapshot (providers.AntigravityIdentitySnapshotJSON) under
// data.providerSpecificData.antigravityIdentity — the lazy-backfill
// write of fragment 972. First writer wins for the account's lifetime:
// the WHERE guard turns the update into a no-op when a pin is already
// stored, so two first touches racing (or a touch on a connection the
// OAuth flow already pinned) can never move an established identity.
// The first pair re-stores providerSpecificData as itself-or-an-empty
// object because jsonb_set cannot create the intermediate object of a
// nested path on a row that has no PSD yet.
func (r *Repo) SetConnectionAntigravityIdentity(connID, identityJSON string) error {
	_, err := r.Exec(
		`UPDATE providerConnections SET data = `+jsonSetExpr("data",
			jsonSetPairAt(jsonSetValue(`COALESCE(data->'providerSpecificData', '{}'::jsonb)`), "providerSpecificData"),
			jsonSetPairAt(jsonDocParam(), "providerSpecificData", "antigravityIdentity"),
		)+` WHERE id = ? AND (data #> '{providerSpecificData,antigravityIdentity}') IS NULL`,
		identityJSON, connID,
	)
	if err != nil {
		return fmt.Errorf("set connection antigravity identity %s: %w", connID, err)
	}
	r.invalidateProviderConnections()
	return nil
}

// SetConnectionCodebuddyIntlIdentity persists the identity the
// codebuddy-intl backfill extracted from a connection's stored JWT
// (fragment 977): the email lands in both the data blob and the email
// column (the two places readers look), the display name in the data
// blob, and — when renameTo is non-empty — the row's display name,
// replacing a generic "Account N". Empty arguments leave their field
// untouched, so the write only ever fills gaps: a row that already
// carries identity is a no-op, which is what makes the backfill safe
// to re-run.
func (r *Repo) SetConnectionCodebuddyIntlIdentity(connID, email, displayName, renameTo string) error {
	var pairs []jsonSetPair
	var args []any
	if email != "" {
		pairs = append(pairs, jsonSetPairAt(jsonStringParam(), "email"))
		args = append(args, email)
	}
	if displayName != "" {
		pairs = append(pairs, jsonSetPairAt(jsonStringParam(), "displayName"))
		args = append(args, displayName)
	}
	sets := make([]string, 0, 4)
	if len(pairs) > 0 {
		sets = append(sets, "data = "+jsonSetExpr("data", pairs...))
	}
	if email != "" {
		sets = append(sets, "email = ?")
		args = append(args, email)
	}
	if renameTo != "" {
		sets = append(sets, "name = ?")
		args = append(args, renameTo)
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updatedAt = ?")
	args = append(args, nowUTC(), connID)
	if _, err := r.Exec(
		`UPDATE providerConnections SET `+strings.Join(sets, ", ")+` WHERE id = ?`,
		args...,
	); err != nil {
		return fmt.Errorf("set connection codebuddy-intl identity %s: %w", connID, err)
	}
	r.invalidateProviderConnections()
	return nil
}

// DisableConnectionWithReason marks a connection as inactive (isActive = 0) and
// records the fatal error message in data.lastError so the dashboard reflects it
// and routing stops picking this connection entirely.
func (r *Repo) DisableConnectionWithReason(id string, status int, reason string) error {
	lowerReason := strings.ToLower(reason)
	if (strings.Contains(lowerReason, "disabled in this account") ||
		strings.Contains(lowerReason, "terms of service") ||
		strings.Contains(lowerReason, "tos_violation") ||
		strings.Contains(lowerReason, "account disabled")) &&
		!strings.Contains(reason, "https://forms.gle/hGzM9MEUv2azZsrb9") {
		reason = strings.TrimRight(reason, ".") + ". Appeal form: https://forms.gle/hGzM9MEUv2azZsrb9"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	lastError, err := json.Marshal(map[string]any{
		"status":    status,
		"message":   reason,
		"timestamp": now,
	})
	if err != nil {
		return fmt.Errorf("marshal last error for %s: %w", id, err)
	}
	_, err = r.Exec(
		`UPDATE providerConnections
		 SET isActive = 0,
		     data = `+jsonSetExpr("data",
			jsonSetPairAt(jsonDocParam(), "lastError"),
			jsonSetPairAt(jsonStringLiteral("disabled"), "status"),
			jsonSetPairAt(jsonStringLiteral("disabled"), "testStatus"),
		)+`,
		     updatedAt = ?
		 WHERE id = ?`,
		string(lastError), now, id,
	)
	if err != nil {
		return fmt.Errorf("disable connection %s: %w", id, err)
	}
	r.invalidateProviderConnections()
	return nil
}

// SetConnectionVerificationRequired flags the connection as
// verification-gated and disables it so traffic rotates to other
// accounts. Only the flag is stored — never the verification URL: the
// link's plt token is minted per refusal and only resolves while the
// operator's browser holds a live sign-in session for the account, so
// a stored copy is a dead letter by the time it is clicked. The fresh
// URL travels in the test response that minted it and is shown in the
// UI at that moment (fragment 1061). Any legacy stored URL is removed.
func (r *Repo) SetConnectionVerificationRequired(connID, errText string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	lastError, err := json.Marshal(map[string]any{
		"status":    403,
		"message":   errText,
		"timestamp": now,
	})
	if err != nil {
		return err
	}
	_, err = r.Exec(
		`UPDATE providerConnections
		 SET isActive = 0,
		     data = (`+jsonSetExpr("data",
			jsonSetPairAt(jsonDocParam(), "lastError"),
			jsonSetPairAt(jsonStringLiteral("validation_required"), "status"),
			jsonSetPairAt(jsonStringLiteral("disabled"), "testStatus"),
		)+`) - 'verificationUrl',
		     updatedAt = ?
		 WHERE id = ?`,
		string(lastError), now, connID,
	)
	if err != nil {
		return fmt.Errorf("set connection verification required %s: %w", connID, err)
	}
	r.invalidateProviderConnections()
	return nil
}

// LockConnectionModel stores a per-connection model lock in the
// providerConnections.data JSON blob using a json_set-style update.
// Also stores backoffLevel. Matches Next.js markAccountUnavailable.
// The stored data is readable by the shared Next.js dashboard.
func (r *Repo) LockConnectionModel(connID, model string, durationSec int, backoffLevel int) error {
	lockedUntil := time.Now().UTC().Add(time.Duration(durationSec) * time.Second).Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.Exec(
		`UPDATE providerConnections SET data = `+jsonSetExpr("data",
			jsonSetBoundPair(jsonStringParam()),
			jsonSetPairAt(jsonIntParam(), "backoffLevel"),
		)+`, updatedAt = ? WHERE id = ?`,
		modelLockPrefix+model, lockedUntil, backoffLevel, now, connID,
	)
	if err != nil {
		return fmt.Errorf("lock connection model %s/%s: %w", connID, model, err)
	}
	r.patchCachedConnectionData(connID, func(_ *models.ProviderConnection, raw map[string]any) {
		raw[modelLockPrefix+model] = lockedUntil
		raw["backoffLevel"] = backoffLevel
	})
	// Mirror the lock into the speed layer so the selector's per-model
	// check can answer from the shared cache instead of a SELECT per
	// candidate (see speed.go). The row above stays the truth.
	if until, perr := time.Parse(time.RFC3339, lockedUntil); perr == nil {
		r.publishModelLock(connID, model, until)
	}
	return nil
}

// LockConnectionModelGone is LockConnectionModel for the banned class
// (a 404 NOT_FOUND and its sibling sentences): the upstream's verdict
// is that this account does not have the model at all, so alongside the
// ordinary model lock it stores a durable modelGone_ twin with the same
// expiry. Selection reads both, but the quota auto-release's bulk clear
// only lifts modelLock_ keys, so a healthy quota read can no longer
// resurrect the model before its sentence ends.
func (r *Repo) LockConnectionModelGone(connID, model string, durationSec int, backoffLevel int) error {
	lockedUntil := time.Now().UTC().Add(time.Duration(durationSec) * time.Second).Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.Exec(
		`UPDATE providerConnections SET data = `+jsonSetExpr("data",
			jsonSetBoundPair(jsonStringParam()),
			jsonSetBoundPair(jsonStringParam()),
			jsonSetPairAt(jsonIntParam(), "backoffLevel"),
		)+`, updatedAt = ? WHERE id = ?`,
		modelLockPrefix+model, lockedUntil, modelGonePrefix+model, lockedUntil, backoffLevel, now, connID,
	)
	if err != nil {
		return fmt.Errorf("lock connection model gone %s/%s: %w", connID, model, err)
	}
	r.patchCachedConnectionData(connID, func(_ *models.ProviderConnection, raw map[string]any) {
		raw[modelLockPrefix+model] = lockedUntil
		raw[modelGonePrefix+model] = lockedUntil
		raw["backoffLevel"] = backoffLevel
	})
	if until, perr := time.Parse(time.RFC3339, lockedUntil); perr == nil {
		r.publishModelLock(connID, model, until)
	}
	return nil
}

// ConnectionCooldownUntil reads the account-scoped quota cooldown a connection
// carries in its data blob, mirroring upstream filterAvailableAccounts
// (open-sse/services/accountFallback.js:180), which skips any account whose
// rateLimitedUntil is still in the future.
//
// Unlike the model lock this cooldown is not keyed by model, which is what
// catches a quota spent account-wide, and it is the only quota cooldown the
// selector can consult when the request carries no model at all. An account
// parked for a rejected OAuth grant carries its own timestamp instead; read
// both through ConnectionBlockedUntil.
//
// A missing, empty or unparseable value reports "not in cooldown" so a
// malformed field can never take routing down.
func ConnectionCooldownUntil(rawData string) (time.Time, bool) {
	raw, ok := parseConnData(rawData)
	if !ok {
		return time.Time{}, false
	}
	return readTimestampField(raw, "rateLimitedUntil")
}

// RecordConnectionError stores the error message, status, and backoffLevel on
// the connection row for dashboard visibility without setting an account-level
// rateLimitedUntil cooldown.
func (r *Repo) RecordConnectionError(connID string, status int, errText string, backoffLevel int) error {
	return r.RecordConnectionScopedError(connID, "", "", status, errText, backoffLevel)
}

// RecordConnectionScopedError stores the error message, status, model, and source
// on the connection row for dashboard visibility without setting rateLimitedUntil.
func (r *Repo) RecordConnectionScopedError(connID, model, source string, status int, errText string, backoffLevel int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	lastError := map[string]any{
		"status":    status,
		"message":   errText,
		"timestamp": now,
	}
	if model != "" {
		lastError["model"] = model
	}
	if source != "" {
		lastError["source"] = source
	}
	payload, err := json.Marshal(lastError)
	if err != nil {
		return fmt.Errorf("marshal last error for %s: %w", connID, err)
	}
	_, err = r.Exec(
		`UPDATE providerConnections
		 SET data = `+jsonSetExpr("data",
			jsonSetPairAt(jsonIntParam(), "backoffLevel"),
			jsonSetPairAt(jsonDocParam(), "lastError"),
			jsonSetPairAt(jsonStringLiteral("error"), "status"),
		)+`,
		     updatedAt = ?
		 WHERE id = ?`,
		backoffLevel, string(payload), now, connID,
	)
	if err != nil {
		return fmt.Errorf("record connection error %s: %w", connID, err)
	}
	r.patchCachedConnectionData(connID, func(_ *models.ProviderConnection, raw map[string]any) {
		raw["backoffLevel"] = backoffLevel
		raw["lastError"] = lastError
		raw["status"] = "error"
	})
	return nil
}

// LockConnectionRateLimit stores the account-scoped cooldown and the error
// that caused it, mirroring upstream applyErrorState
// (open-sse/services/accountFallback.js:216). The dashboard already reads
// rateLimitedUntil to render "reset at", but nothing in the Go port ever
// wrote it, so that field was always empty and the selector had no
// account-scoped signal to skip on.
func (r *Repo) LockConnectionRateLimit(connID string, until time.Time, backoffLevel, status int, errText string) error {
	nowT := time.Now().UTC()
	now := nowT.Format(time.RFC3339)
	lastError := map[string]any{
		"status":    status,
		"message":   errText,
		"timestamp": now,
	}
	payload, err := json.Marshal(lastError)
	if err != nil {
		return fmt.Errorf("marshal last error for %s: %w", connID, err)
	}
	// Fragment 974: roll this episode's re-eligibility jitter in the same
	// statement that stores the cooldown (see accounts_pacing.go), so the
	// announced rateLimitedUntil stays truthful while selection honors
	// the jittered companion. A write with no jitter stores a null
	// companion, retiring any roll an older episode left behind.
	eligiblePair := jsonSetPairAt(jsonNullSQLValue(), keyRateLimitEligibleAt)
	pairs := []jsonSetPair{
		jsonSetPairAt(jsonStringParam(), "rateLimitedUntil"),
		jsonSetPairAt(jsonIntParam(), "backoffLevel"),
		jsonSetPairAt(jsonDocParam(), "lastError"),
		jsonSetPairAt(jsonStringLiteral("error"), "status"),
	}
	untilStr := until.UTC().Format(time.RFC3339)
	var eligibleVal any
	args := []any{untilStr, backoffLevel, string(payload)}
	if eligibleAt, ok := r.reentryEligibleAt(connID, nowT, until); ok {
		eligiblePair = jsonSetPairAt(jsonStringParam(), keyRateLimitEligibleAt)
		eligibleVal = eligibleAt.UTC().Format(time.RFC3339)
		args = append(args, eligibleVal)
	}
	pairs = append(pairs, eligiblePair)
	args = append(args, now, connID)
	_, err = r.Exec(
		`UPDATE providerConnections
		 SET data = `+jsonSetExpr("data", pairs...)+`,
		     updatedAt = ?
		 WHERE id = ?`,
		args...,
	)
	if err != nil {
		return fmt.Errorf("lock connection rate limit %s: %w", connID, err)
	}
	r.patchCachedConnectionData(connID, func(_ *models.ProviderConnection, raw map[string]any) {
		raw["rateLimitedUntil"] = untilStr
		raw["backoffLevel"] = backoffLevel
		raw["lastError"] = lastError
		raw["status"] = "error"
		raw[keyRateLimitEligibleAt] = eligibleVal
	})
	return nil
}

// ClearConnectionRateLimit drops the account-scoped cooldown after a request
// was served, mirroring upstream resetAccountState
// (open-sse/services/accountFallback.js:198). Without it a recovered account
// would stay skipped until the cooldown expired on its own. The per-model
// locks are deliberately left alone: a success on one model must not unlock a
// model that is still in cooldown.
func (r *Repo) ClearConnectionRateLimit(connID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.Exec(
		`UPDATE providerConnections
		 SET data = `+jsonSetExpr("data",
			jsonSetPairAt(jsonNullSQLValue(), "rateLimitedUntil"),
			jsonSetPairAt(jsonNullSQLValue(), keyRateLimitEligibleAt),
			jsonSetPairAt(jsonNullSQLValue(), "lastError"),
			jsonSetPairAt(jsonStringLiteral("active"), "status"),
			jsonSetPairAt(jsonIntLiteral(0), "backoffLevel"),
		)+`,
		     updatedAt = ?
		 WHERE id = ?`,
		now, connID,
	)
	if err != nil {
		return fmt.Errorf("clear connection rate limit %s: %w", connID, err)
	}
	r.patchCachedConnectionData(connID, func(_ *models.ProviderConnection, raw map[string]any) {
		raw["rateLimitedUntil"] = nil
		raw[keyRateLimitEligibleAt] = nil
		raw["lastError"] = nil
		raw["status"] = "active"
		raw["backoffLevel"] = 0
	})
	return nil
}

// ClearConnectionSuccessState performs the chat success path's three
// clears — UnlockConnectionModel for the canonical lock key and the
// raw model, plus ClearConnectionRateLimit — as ONE statement whose
// guard makes it a no-op when the connection carries no state to
// clear.
//
// Why (review cycle 1, F1): the three clears used to run as separate
// unconditional UPDATEs on EVERY successful request, all against the
// same providerConnections row. Under concurrency they serialised the
// whole gateway on that row's lock (pg_stat_activity: 9 of 10 pooled
// backends parked in Lock/transactionid), and each was its own WAL
// flush. The fused statement writes the exact union of the three
// clears' effects, but its WHERE only matches when at least one piece
// of clearable state is present — a model lock (either key), an
// account cooldown, a recorded error, or a raised backoff level, which
// between them cover every writer of that state in this package. One
// deliberate narrowing of the union (council round 1, C-4): the
// status-to-"active" stamp lands only on a row the operator has not
// disabled; the field clears apply either way. In
// the overwhelmingly common case (nothing was ever locked) the
// statement matches zero rows: a primary-key probe, no row lock taken,
// no WAL record written.
//
// The speed-layer overlay entries for both lock keys are deleted
// unconditionally, exactly as UnlockConnectionModel does: an overlay
// entry can outlive its row state when another writer cleared the
// row, and deleting a positive shortcut can only cost a re-read.
func (r *Repo) ClearConnectionSuccessState(connID, lockKey, model string) error {
	keys := []string{lockKey}
	if model != "" && model != lockKey {
		keys = append(keys, model)
	}
	pairs := make([]jsonSetPair, 0, len(keys)+4)
	setArgs := make([]any, 0, len(keys))
	guardArgs := make([]any, 0, len(keys))
	guards := make([]string, 0, len(keys)+3)
	for _, k := range keys {
		// A proven success resurrects the pair outright: both the
		// transient lock and its durable modelGone_ twin clear.
		pairs = append(pairs, jsonSetBoundPair(jsonNullLiteral()), jsonSetBoundPair(jsonNullLiteral()))
		setArgs = append(setArgs, modelLockPrefix+k, modelGonePrefix+k)
		guards = append(guards, `(data #>> ARRAY[?]) IS NOT NULL`, `(data #>> ARRAY[?]) IS NOT NULL`)
		guardArgs = append(guardArgs, modelLockPrefix+k, modelGonePrefix+k)
	}
	// The status stamp is conditional (council round 1, C-4): a success
	// that was already in flight when the operator disabled the
	// connection must still clear its locks and cooldowns, but it must
	// not flip the row's status back to "active" — the disable would
	// be silently undone. "Disabled" is the row's own derivation, the
	// same triple flattenConnection reads: the isActive column, a
	// disabledAt stamp, or a disabled testStatus. A disabled row keeps
	// its stored status; the COALESCE covers a row carrying clearable
	// state but no status key yet (a bare model lock is such a state),
	// where a JSON null reads as "no status" to every consumer. Like
	// every column reference in a SET clause, the CASE reads the
	// pre-update row.
	statusValue := jsonSetValue(`(CASE WHEN isActive = 1 AND (data #>> '{disabledAt}') IS NULL AND COALESCE(data #>> '{testStatus}', '') <> 'disabled' THEN ` +
		string(jsonStringLiteral("active")) +
		` ELSE COALESCE(data #> '{status}', 'null'::jsonb) END)`)
	pairs = append(pairs,
		jsonSetPairAt(jsonNullSQLValue(), "rateLimitedUntil"),
		jsonSetPairAt(jsonNullSQLValue(), keyRateLimitEligibleAt),
		jsonSetPairAt(jsonNullSQLValue(), "lastError"),
		jsonSetPairAt(statusValue, "status"),
		jsonSetPairAt(jsonIntLiteral(0), "backoffLevel"),
	)
	guards = append(guards,
		`(data #>> '{rateLimitedUntil}') IS NOT NULL`,
		`(data #>> '{lastError}') IS NOT NULL`,
		`COALESCE(data #>> '{backoffLevel}', '0') <> '0'`,
	)
	now := time.Now().UTC().Format(time.RFC3339)
	query := `UPDATE providerConnections SET data = ` + jsonSetExpr("data", pairs...) +
		`, updatedAt = ? WHERE id = ? AND (` + strings.Join(guards, " OR ") + `)`
	args := make([]any, 0, len(setArgs)+2+len(guardArgs))
	args = append(args, setArgs...)
	args = append(args, now, connID)
	args = append(args, guardArgs...)
	res, err := r.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("clear connection success state %s: %w", connID, err)
	}
	for _, k := range keys {
		r.deleteModelLockOverlay(connID, k)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		r.patchCachedConnectionData(connID, func(conn *models.ProviderConnection, raw map[string]any) {
			for _, k := range keys {
				raw[modelLockPrefix+k] = nil
				raw[modelGonePrefix+k] = nil
			}
			raw["rateLimitedUntil"] = nil
			raw[keyRateLimitEligibleAt] = nil
			raw["lastError"] = nil
			raw["backoffLevel"] = 0
			// Mirror the SQL's conditional status stamp (C-4): a
			// disabled row keeps its stored status.
			testStatus, _ := raw["testStatus"].(string)
			if conn.IsActive == 1 && raw["disabledAt"] == nil && testStatus != "disabled" {
				raw["status"] = "active"
			}
		})
	}
	return nil
}

// ClearConnectionModelLocks drops every per-model lock a connection carries,
// leaving the rest of its data alone. A renewed credential makes the old
// locks moot: without this an account that was parked for a ban or a mute
// stays parked even after the operator re-authenticates.
func (r *Repo) ClearConnectionModelLocks(ctx context.Context, connID string) error {
	var rawData string
	if err := r.QueryRowContext(ctx, "SELECT data FROM providerConnections WHERE id = ?", connID).Scan(&rawData); err != nil {
		return fmt.Errorf("clear connection model locks %s: %w", connID, err)
	}
	var dataMap map[string]any
	if err := json.Unmarshal([]byte(rawData), &dataMap); err != nil {
		return fmt.Errorf("clear connection model locks %s: %w", connID, err)
	}
	changed := false
	for k := range dataMap {
		if strings.HasPrefix(k, modelLockPrefix) && dataMap[k] != nil {
			dataMap[k] = nil
			changed = true
		}
	}
	if !changed {
		return nil
	}
	encoded, err := json.Marshal(dataMap)
	if err != nil {
		return fmt.Errorf("clear connection model locks %s: %w", connID, err)
	}
	if err := r.UpdateConnectionData(connID, string(encoded)); err != nil {
		return err
	}
	r.clearModelLockOverlay(connID)
	return nil
}

// IsConnectionModelLocked checks whether the given connection has an active
// modelLock_<model> field in its data JSON blob. Returns true when the
// timestamp is in the future. A live modelGone_<model> marker (the durable
// half of a banned-class lock) locks the pair the same way.
func isConnectionModelLockedFromData(rawData, model string) bool {
	if rawData == "" {
		return false
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(rawData), &raw); err != nil {
		return false
	}
	return lockTimestampLive(raw, modelLockPrefix+model) || lockTimestampLive(raw, modelGonePrefix+model)
}

// lockTimestampLive reports whether raw[key] holds an RFC3339 timestamp
// still in the future.
func lockTimestampLive(raw map[string]any, key string) bool {
	val, ok := raw[key]
	if !ok {
		return false
	}
	str, ok := val.(string)
	if !ok || str == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, str)
	if err != nil {
		return false
	}
	return time.Now().UTC().Before(t)
}

func (r *Repo) IsConnectionModelLocked(connID, model string) (bool, error) {
	// Speed-layer shortcut (P3, see speed.go): a live overlay entry is a
	// lock a writer already persisted to the row below, so answering
	// from it skips this SELECT on the selection hot path. A miss falls
	// through to the row, which stays authoritative.
	if r.overlayModelLocked(connID, model) {
		return true, nil
	}
	// Check cached in-memory connection snapshot first
	if cached, ok := r.GetCachedConnectionByID("", connID); ok && cached != nil {
		return isConnectionModelLockedFromData(cached.Data, model), nil
	}
	var rawData string
	err := r.QueryRow("SELECT data FROM providerConnections WHERE id = ?", connID).Scan(&rawData)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get connection data %s: %w", connID, err)
	}

	return isConnectionModelLockedFromData(rawData, model), nil
}

// UnlockConnectionModel removes a per-connection model lock by setting
// modelLock_<model> to null and resetting backoffLevel to 0. An explicit
// per-model unlock is an operator decision, so it also lifts the durable
// modelGone_<model> twin. Matches Next.js clearAccountError behavior.
func (r *Repo) UnlockConnectionModel(connID, model string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.Exec(
		`UPDATE providerConnections SET data = `+jsonSetExpr("data",
			jsonSetBoundPair(jsonNullLiteral()),
			jsonSetBoundPair(jsonNullLiteral()),
			jsonSetPairAt(jsonIntLiteral(0), "backoffLevel"),
		)+`, updatedAt = ? WHERE id = ?`,
		modelLockPrefix+model, modelGonePrefix+model, now, connID,
	)
	if err != nil {
		return fmt.Errorf("unlock connection model %s/%s: %w", connID, model, err)
	}
	r.patchCachedConnectionData(connID, func(_ *models.ProviderConnection, raw map[string]any) {
		raw[modelLockPrefix+model] = nil
		raw[modelGonePrefix+model] = nil
		raw["backoffLevel"] = 0
	})
	r.deleteModelLockOverlay(connID, model)
	return nil
}

// ResetConnectionHealthState clears model locks, error codes, rate limits, and resets backoff
// on a connection when activated or re-validated (parity with #3830 / #7fee56ba).
func (r *Repo) ResetConnectionHealthState(connID string) error {
	var rawData string
	err := r.QueryRow("SELECT data FROM providerConnections WHERE id = ?", connID).Scan(&rawData)
	if err != nil {
		return err
	}
	var dataMap map[string]any
	if err := json.Unmarshal([]byte(rawData), &dataMap); err != nil {
		return err
	}

	dataMap["errorCode"] = nil
	dataMap["rateLimitedUntil"] = nil
	dataMap[keyRateLimitEligibleAt] = nil
	dataMap["oauthLockedUntil"] = nil
	dataMap[keyOAuthLockEligibleAt] = nil
	dataMap["oauthFailureCount"] = 0
	dataMap["backoffLevel"] = 0

	for k := range dataMap {
		if isModelLockKey(k) {
			dataMap[k] = nil
		}
	}

	newBytes, err := json.Marshal(dataMap)
	if err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = r.Exec("UPDATE providerConnections SET data = ?, updatedAt = ? WHERE id = ?", string(newBytes), now, connID)
	if err == nil {
		r.invalidateProviderConnections()
		r.clearModelLockOverlay(connID)
	}
	return err
}

// GetConnectionBackoffLevel reads the backoffLevel from a connection's data.
// Returns 0 when not set or on error.
func (r *Repo) GetConnectionBackoffLevel(connID string) int {
	var rawData string
	err := r.QueryRow("SELECT data FROM providerConnections WHERE id = ?", connID).Scan(&rawData)
	if err != nil {
		return 0
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(rawData), &raw); err != nil {
		return 0
	}
	level, ok := raw["backoffLevel"].(float64)
	if !ok {
		return 0
	}
	return int(level)
}

// IsProviderAvailable returns true if at least one active connection for the
// given provider has no active modelLock_<model>. This is the connection-based
// replacement for the old kv-based IsProviderHealthy, matching Next.js's
// isModelLockActive + filterAvailableAccounts flow.
// Returns true when no connections exist (optimistic default for no-auth providers).
func (r *Repo) IsProviderAvailable(provider, model string) bool {
	if provider == "" {
		return true
	}
	conns, err := r.GetProviderConnections(provider, true)
	if err != nil || len(conns) == 0 {
		return true
	}
	for _, c := range conns {
		if c == nil {
			continue
		}
		if r.overlayModelLocked(c.ID, model) {
			continue
		}
		if !isConnectionModelLockedFromData(c.Data, model) {
			return true
		}
	}
	return false
}

// ResetProviderHealth clears modelLock_* fields on connections, matching
// Next.js clearAccountError semantics.
//   - provider="" and model="" → all connections
//   - model="" → all connections for provider
//   - both set → specific provider + model on all its connections
func (r *Repo) ResetProviderHealth(provider, model string) error {
	if provider == "" && model == "" {
		return r.clearAllModelLocks()
	}
	if model == "" {
		return r.clearProviderModelLocks(provider)
	}
	return r.clearSpecificModelLock(provider, model)
}

// listConnectionData drains a providerConnections id/data query and closes
// its rows before returning. The clear*ModelLocks helpers write per row
// (clearConnectionModelLocks / Exec), and a nested pool checkout while the
// SELECT rows are still open can deadlock the pool the same way
// GetProviderConnections did: the writer waits for a connection the open
// rows will never release. Collect first, then write.
func (r *Repo) listConnectionData(query string, args ...any) ([]struct{ id, data string }, error) {
	rows, err := r.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []struct{ id, data string }
	for rows.Next() {
		var row struct{ id, data string }
		if err := rows.Scan(&row.id, &row.data); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repo) clearAllModelLocks() error {
	pairs, err := r.listConnectionData("SELECT id, data FROM providerConnections")
	if err != nil {
		return err
	}
	for _, p := range pairs {
		if err := r.clearConnectionModelLocks(p.id, p.data); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) clearProviderModelLocks(provider string) error {
	pairs, err := r.listConnectionData("SELECT id, data FROM providerConnections WHERE provider = ?", provider)
	if err != nil {
		return err
	}
	for _, p := range pairs {
		if err := r.clearConnectionModelLocks(p.id, p.data); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) clearSpecificModelLock(provider, model string) error {
	key := modelLockPrefix + model
	pairs, err := r.listConnectionData("SELECT id, data FROM providerConnections WHERE provider = ?", provider)
	if err != nil {
		return err
	}
	for _, p := range pairs {
		locked := checkConnectionModelLock(p.data, model)
		if !locked {
			continue
		}
		if _, err := r.Exec(
			`UPDATE providerConnections SET data = `+jsonSetExpr("data",
				jsonSetBoundPair(jsonNullLiteral()),
				jsonSetPairAt(jsonIntLiteral(0), "backoffLevel"),
			)+`, updatedAt = ? WHERE id = ?`,
			modelLockPrefix+model, nowUTC(), p.id,
		); err != nil {
			return fmt.Errorf("clear lock %s on %s: %w", key, p.id, err)
		}
	}
	return nil
}

func (r *Repo) clearConnectionModelLocks(id, data string) error {
	fields := listModelLockJSONKeys(data)
	if len(fields) == 0 {
		return nil
	}
	pairs := make([]jsonSetPair, 0, len(fields)+1)
	args := make([]any, 0, len(fields)+2)
	for _, f := range fields {
		pairs = append(pairs, jsonSetBoundPair(jsonNullLiteral()))
		args = append(args, f)
	}
	pairs = append(pairs, jsonSetPairAt(jsonIntLiteral(0), "backoffLevel"))
	q := "UPDATE providerConnections SET data = " +
		jsonSetExpr("data", pairs...) +
		", updatedAt = ? WHERE id = ?"
	args = append(args, nowUTC(), id)
	_, err := r.Exec(q, args...)
	return err
}

// checkConnectionModelLock parses connection data to check if the pair
// carries a live lock of either family (modelLock_ or modelGone_).
func checkConnectionModelLock(data string, model string) bool {
	return isConnectionModelLockedFromData(data, model)
}

// listModelLockJSONKeys returns all top-level JSON keys carrying per-model
// lock state of either family (modelLock_ or modelGone_).
func listModelLockJSONKeys(data string) []string {
	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil
	}
	var keys []string
	for k := range raw {
		if isModelLockKey(k) && len(k) > len(modelLockPrefix) {
			keys = append(keys, k)
		}
	}
	return keys
}
