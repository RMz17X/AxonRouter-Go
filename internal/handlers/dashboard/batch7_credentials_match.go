package dashboard

import (
	"context"
	"fmt"
	"strings"

	json "encoding/json/v2"

	"github.com/google/uuid"
)

// Connection matcher + import orchestration (batch 7, AxonRouter parity):
// ports src/lib/credentials/matcher.tsx and importer.ts. See
// batch7_credentials_import.go for the normalize/validate stages.

// credConn is one existing connection in the matcher's index.
type credConn struct {
	flat map[string]any
}

func (c *credConn) id() string { return recStr(c.flat, "id") }

// credentialMatcher indexes existing connections for the five match
// strategies, in upstream order: source id, OAuth email, name,
// single-OAuth fallback, token fingerprint.
type credentialMatcher struct {
	byId          map[string]*credConn
	byEmail       map[string]*credConn
	byName        map[string][]*credConn
	byProvider    map[string][]*credConn
	byFingerprint map[string]*credConn
	processed     map[string]bool
}

func newCredentialMatcher(flats []map[string]any) *credentialMatcher {
	m := &credentialMatcher{
		byId:          map[string]*credConn{},
		byEmail:       map[string]*credConn{},
		byName:        map[string][]*credConn{},
		byProvider:    map[string][]*credConn{},
		byFingerprint: map[string]*credConn{},
		processed:     map[string]bool{},
	}
	for _, flat := range flats {
		m.addConnection(&credConn{flat: flat})
	}
	return m
}

// tokenFingerprintFields is the fingerprint field priority order.
var tokenFingerprintFields = []string{"accessToken", "refreshToken", "idToken", "apiKey", "projectId"}

// pickTokenFingerprint returns "<field>:<value>" for the first non-empty
// string token field, or "".
func pickTokenFingerprint(record map[string]any) string {
	if record == nil {
		return ""
	}
	for _, field := range tokenFingerprintFields {
		if v, ok := record[field].(string); ok && v != "" {
			return field + ":" + v
		}
	}
	return ""
}

func (m *credentialMatcher) addConnection(conn *credConn) {
	if conn == nil || conn.id() == "" {
		return
	}
	flat := conn.flat
	m.byId[conn.id()] = conn
	if recStr(flat, "authType") == "oauth" && recStr(flat, "email") != "" {
		m.byEmail[recStr(flat, "provider")+":"+recStr(flat, "email")] = conn
	}
	if recStr(flat, "name") != "" {
		key := recStr(flat, "provider") + ":" + recStr(flat, "authType") + ":" + recStr(flat, "name")
		m.byName[key] = append(m.byName[key], conn)
	}
	provider := recStr(flat, "provider")
	m.byProvider[provider] = append(m.byProvider[provider], conn)
	if fp := pickTokenFingerprint(flat); fp != "" {
		m.byFingerprint[provider+":"+fp] = conn
	}
}

func (m *credentialMatcher) firstUnprocessed(list []*credConn) *credConn {
	for _, conn := range list {
		if conn != nil && !m.processed[conn.id()] {
			return conn
		}
	}
	return nil
}

// findMatch runs the five strategies in upstream order.
func (m *credentialMatcher) findMatch(record map[string]any, sourceID string) *credConn {
	provider := recStr(record, "provider")
	authType := recStr(record, "authType")

	if sourceID != "" {
		if conn := m.byId[sourceID]; conn != nil &&
			recStr(conn.flat, "provider") == provider &&
			recStr(conn.flat, "authType") == authType &&
			!m.processed[conn.id()] {
			return conn
		}
	}

	if authType == "oauth" && recStr(record, "email") != "" {
		if conn := m.byEmail[provider+":"+recStr(record, "email")]; conn != nil && !m.processed[conn.id()] {
			return conn
		}
	}

	if recStr(record, "name") != "" {
		key := provider + ":" + authType + ":" + recStr(record, "name")
		if matched := m.firstUnprocessed(m.byName[key]); matched != nil {
			return matched
		}
	}

	if authType == "oauth" && recStr(record, "email") == "" && recStr(record, "name") == "" {
		unprocessed := []*credConn{}
		for _, conn := range m.byProvider[provider] {
			if recStr(conn.flat, "authType") == "oauth" && !m.processed[conn.id()] {
				unprocessed = append(unprocessed, conn)
			}
		}
		if len(unprocessed) == 1 {
			return unprocessed[0]
		}
	}

	if authType == "oauth" && recStr(record, "email") == "" && recStr(record, "name") == "" {
		if fp := pickTokenFingerprint(record); fp != "" {
			if conn := m.byFingerprint[provider+":"+fp]; conn != nil && !m.processed[conn.id()] {
				return conn
			}
		}
	}

	return nil
}

func (m *credentialMatcher) markProcessed(id string) {
	m.processed[id] = true
}

// updateConnection applies an update to the indexed record and rebuilds
// its index entries, mirroring ConnectionMatcher.updateConnection.
func (m *credentialMatcher) updateConnection(id string, updates map[string]any) {
	conn := m.byId[id]
	if conn == nil {
		return
	}
	flat := conn.flat
	oldProvider := recStr(flat, "provider")

	if recStr(flat, "authType") == "oauth" && recStr(flat, "email") != "" {
		oldKey := oldProvider + ":" + recStr(flat, "email")
		if m.byEmail[oldKey] == conn {
			delete(m.byEmail, oldKey)
		}
	}
	if recStr(flat, "name") != "" {
		oldKey := oldProvider + ":" + recStr(flat, "authType") + ":" + recStr(flat, "name")
		list := m.byName[oldKey]
		for i, entry := range list {
			if entry == conn {
				list = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(list) == 0 {
			delete(m.byName, oldKey)
		} else {
			m.byName[oldKey] = list
		}
	}
	if fp := pickTokenFingerprint(flat); fp != "" {
		oldKey := oldProvider + ":" + fp
		if m.byFingerprint[oldKey] == conn {
			delete(m.byFingerprint, oldKey)
		}
	}

	for k, v := range updates {
		flat[k] = v
	}

	if newProvider := recStr(flat, "provider"); newProvider != oldProvider {
		list := m.byProvider[oldProvider]
		for i, entry := range list {
			if entry.id() == id {
				m.byProvider[oldProvider] = append(list[:i], list[i+1:]...)
				break
			}
		}
		m.byProvider[newProvider] = append(m.byProvider[newProvider], conn)
	}

	if recStr(flat, "authType") == "oauth" && recStr(flat, "email") != "" {
		m.byEmail[recStr(flat, "provider")+":"+recStr(flat, "email")] = conn
	}
	if recStr(flat, "name") != "" {
		key := recStr(flat, "provider") + ":" + recStr(flat, "authType") + ":" + recStr(flat, "name")
		list := m.byName[key]
		found := false
		for _, entry := range list {
			if entry == conn {
				found = true
			}
		}
		if !found {
			m.byName[key] = append(list, conn)
		}
	}
	if fp := pickTokenFingerprint(flat); fp != "" {
		m.byFingerprint[recStr(flat, "provider")+":"+fp] = conn
	}
}

// duplicateImport is one validateNoDuplicateImports finding.
type duplicateImport struct {
	index      int
	firstIndex int
	key        string
}

// validateNoDuplicateImports ports the function of the same name. Indexes
// are positions in the sanitized record array.
func validateNoDuplicateImports(records []map[string]any) []duplicateImport {
	seen := map[string]int{}
	duplicates := []duplicateImport{}
	for i, record := range records {
		var key string
		switch {
		case recStr(record, "authType") == "oauth" && recStr(record, "email") != "":
			key = recStr(record, "provider") + ":oauth:" + recStr(record, "email")
		case recStr(record, "name") != "":
			key = recStr(record, "provider") + ":" + recStr(record, "authType") + ":" + recStr(record, "name")
		case recStr(record, "authType") == "oauth":
			fp := pickTokenFingerprint(record)
			if fp == "" {
				continue
			}
			key = recStr(record, "provider") + ":oauth:fp:" + fp
		default:
			continue
		}
		if first, ok := seen[key]; ok {
			duplicates = append(duplicates, duplicateImport{index: i, firstIndex: first, key: key})
		} else {
			seen[key] = i
		}
	}
	return duplicates
}

// --- storage split ---------------------------------------------------------

// credentialColumnFields are the record fields the providerConnections
// columns own; everything else in a record lives in the data blob.
var credentialColumnFields = map[string]bool{
	"id": true, "provider": true, "authType": true, "name": true,
	"email": true, "priority": true, "isActive": true,
	"lastUsedAt": true, "consecutiveUseCount": true,
}

// splitCredentialRecord divides a record into its column values and its
// data-blob JSON.
func splitCredentialRecord(record map[string]any) (name string, email *string, priority *int, isActive bool, dataJSON string) {
	name = recStr(record, "name")
	if s, ok := record["email"].(string); ok {
		email = &s
	}
	if f, ok := record["priority"].(float64); ok {
		p := int(f)
		priority = &p
	}
	isActive = true
	if b, ok := record["isActive"].(bool); ok {
		isActive = b
	}
	data := map[string]any{}
	for k, v := range record {
		if !credentialColumnFields[k] {
			data[k] = v
		}
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return name, email, priority, isActive, "{}"
	}
	return name, email, priority, isActive, string(encoded)
}

// connectionStillValid probes the stored connection without persisting
// the outcome (upstream's testSingleConnection with persistStatus:
// false), reporting whether the stored credential still works.
func (h *DashboardHandler) connectionStillValid(ctx context.Context, id string) bool {
	conn, err := h.Repo.GetProviderConnectionByID(id)
	if err != nil || conn == nil {
		return false
	}
	raw, _ := decodeConnectionData(conn.Data)
	if raw == nil {
		raw = map[string]any{}
	}
	data := parseConnectionProbeData(raw)
	out := h.testSingleConnection(ctx, conn, data, raw)
	return out.valid
}

// --- orchestration -----------------------------------------------------------

type normalizedImportRecord struct {
	index    int
	sourceID string
	data     map[string]any
}

// importCredentials ports importCredentials from importer.ts.
func (h *DashboardHandler) importCredentials(ctx context.Context, payload any) (map[string]any, *credImportError) {
	inputRecords := extractInputRecords(payload)
	if inputRecords == nil {
		return nil, &credImportError{
			code:    "INVALID_IMPORT_PAYLOAD",
			message: "Payload must contain credentials array or equivalent entries",
		}
	}

	normalizedRecords := []normalizedImportRecord{}
	skipReasons := []any{}
	for _, item := range inputRecords {
		index := len(normalizedRecords) + len(skipReasons) + 1
		normalized := normalizeInputRecord(item)
		if normalized == nil {
			skipReasons = append(skipReasons, map[string]any{
				"index":   index,
				"code":    "INVALID_RECORD",
				"message": "Credential record must be an object",
			})
			continue
		}
		sanitized, sanitizeErr := sanitizeCredentialRecord(normalized)
		if sanitizeErr != nil {
			skipReasons = append(skipReasons, map[string]any{
				"index":   index,
				"code":    sanitizeErr.code,
				"message": sanitizeErr.message,
			})
			continue
		}
		sourceID := ""
		if s, ok := normalized["id"].(string); ok {
			sourceID = s
		}
		normalizedRecords = append(normalizedRecords, normalizedImportRecord{
			index: index, sourceID: sourceID, data: sanitized,
		})
	}

	datas := make([]map[string]any, len(normalizedRecords))
	for i, rec := range normalizedRecords {
		datas[i] = rec.data
	}
	if duplicates := validateNoDuplicateImports(datas); len(duplicates) > 0 {
		details := make([]string, len(duplicates))
		for i, d := range duplicates {
			details[i] = fmt.Sprintf("Record %d duplicates record %d (%s)", d.index, d.firstIndex, d.key)
		}
		return nil, &credImportError{
			code:    "DUPLICATE_IMPORT_RECORDS",
			message: "Duplicate import records detected: " + strings.Join(details, "; "),
		}
	}

	existing, err := h.Repo.GetProviderConnections("", false)
	if err != nil {
		return nil, &credImportError{code: "IMPORT_FAILED", message: err.Error()}
	}
	flats := make([]map[string]any, len(existing))
	for i, conn := range existing {
		flats[i] = flattenCredentialConnection(conn)
	}
	matcher := newCredentialMatcher(flats)

	created, updated, preserved := 0, 0, 0
	for _, rec := range normalizedRecords {
		data := rec.data
		match := matcher.findMatch(data, rec.sourceID)
		if match != nil {
			matchID := match.id()
			if h.connectionStillValid(ctx, matchID) {
				matcher.markProcessed(matchID)
				preserved++
				continue
			}
			merged := map[string]any{}
			for k, v := range match.flat {
				merged[k] = v
			}
			for k, v := range data {
				merged[k] = v
			}
			name, email, priority, isActive, dataJSON := splitCredentialRecord(merged)
			if err := h.Repo.UpdateImportedConnection(matchID, name, email, priority, isActive, dataJSON); err != nil {
				return nil, &credImportError{code: "IMPORT_FAILED", message: err.Error()}
			}
			matcher.updateConnection(matchID, data)
			matcher.markProcessed(matchID)
			updated++
			continue
		}

		if recStr(data, "authType") == "apikey" && recStr(data, "name") == "" {
			skipReasons = append(skipReasons, map[string]any{
				"code":    "INVALID_RECORD",
				"message": "API key credential record is missing name",
			})
			continue
		}

		newID := uuid.New().String()
		name, email, priority, isActive, dataJSON := splitCredentialRecord(data)
		if err := h.Repo.CreateImportedConnection(newID, recStr(data, "provider"), recStr(data, "authType"),
			name, email, priority, isActive, dataJSON); err != nil {
			return nil, &credImportError{code: "IMPORT_FAILED", message: err.Error()}
		}
		flat := map[string]any{}
		for k, v := range data {
			flat[k] = v
		}
		flat["id"] = newID
		matcher.addConnection(&credConn{flat: flat})
		matcher.markProcessed(newID)
		created++
	}

	return map[string]any{
		"created":     created,
		"updated":     updated,
		"deleted":     0,
		"skipped":     len(skipReasons),
		"imported":    created + updated,
		"preserved":   preserved,
		"skipReasons": skipReasons,
	}, nil
}
