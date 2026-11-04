package oauth

import (
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/handlerutil"
)

// The POST bulk-jwt action of the generic dispatch route (split from
// batch3_dynamic2.go): pasted-token parsing, dedupe, and serial creation
// for the CodeBuddy-family providers.

// ---- bulk-jwt ----

// jwtEntry is one parsed bulk-jwt line/object: a token plus its optional
// display name and refresh token (`password` fields are deliberately never
// read — Axon does not persist them).
type jwtEntry struct {
	name         string
	token        string
	refreshToken string
}

// bulkStringField reads the first non-empty string among keys, mirroring
// JS `obj.a || obj.b || ...` over JSON values.
func bulkStringField(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := obj[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func bulkFromObject(obj map[string]any) jwtEntry {
	return jwtEntry{
		name:         bulkStringField(obj, "name", "email", "username"),
		token:        bulkStringField(obj, "accessToken", "access_token", "token", "apiKey"),
		refreshToken: bulkStringField(obj, "refreshToken", "refresh_token"),
	}
}

// bulkAccountList normalizes a decoded JSON value to an entry list: a bare
// array, a wrapper object's accounts/tokens/data array, or a single object.
func bulkAccountList(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case map[string]any:
		for _, k := range []string{"accounts", "tokens", "data"} {
			if arr, ok := t[k].([]any); ok {
				return arr
			}
		}
		return []any{t}
	}
	return nil
}

// bulkToEntries converts decoded list items to entries: objects map through
// bulkFromObject; strings are bare tokens, `name|token` pairs (split on the
// LAST |), or self-contained JSON objects — a JSON-looking line that yields
// no token is rejected outright, never stored as a raw key.
func bulkToEntries(list []any) []jwtEntry {
	var out []jwtEntry
	for _, item := range list {
		if obj, ok := item.(map[string]any); ok {
			if e := bulkFromObject(obj); len(e.token) > 3 {
				out = append(out, e)
			}
			continue
		}
		s := strings.TrimSpace(bulkStringify(item))
		if s == "" {
			continue
		}
		if strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
			var parsed any
			if err := json.Unmarshal([]byte(s), &parsed); err != nil {
				continue
			}
			obj, ok := parsed.(map[string]any)
			if !ok {
				continue
			}
			if e := bulkFromObject(obj); len(e.token) > 3 {
				out = append(out, e)
			}
			continue
		}
		e := jwtEntry{token: s}
		if sep := strings.LastIndex(s, "|"); sep > 0 {
			e.name = strings.TrimSpace(s[:sep])
			e.token = strings.TrimSpace(s[sep+1:])
		}
		if len(e.token) > 3 {
			out = append(out, e)
		}
	}
	return out
}

// bulkStringify renders a decoded JSON scalar the way JS String() would.
func bulkStringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	return ""
}

// bulkParseConcatenated reads several top-level JSON values pasted back to
// back (whitespace/comma separated) — a pretty-printed export stream that
// no single JSON parse accepts. Returns nil on any malformed input.
func bulkParseConcatenated(text string) []any {
	var values []any
	depth := 0
	inString, escape := false, false
	start := -1
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if inString {
			switch {
			case escape:
				escape = false
			case ch == '\\':
				escape = true
			case ch == '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{', '[':
			if depth == 0 {
				start = i
			}
			depth++
		case '}', ']':
			if depth == 0 {
				return nil
			}
			depth--
			if depth == 0 && start >= 0 {
				var v any
				if err := json.Unmarshal([]byte(text[start:i+1]), &v); err != nil {
					return nil
				}
				values = append(values, v)
				start = -1
			}
		default:
			if depth == 0 && ch != ',' && ch != ' ' && ch != '\t' && ch != '\n' && ch != '\r' {
				return nil
			}
		}
	}
	if inString || depth != 0 || len(values) == 0 {
		return nil
	}
	return values
}

var (
	bulkLineSplitRe  = regexp.MustCompile(`\s*\n+\s*`)
	bulkPieceSplitRe = regexp.MustCompile(`\s{2,}|\t`)
	bulkTokenFieldRe = regexp.MustCompile(`"(accessToken|access_token|refreshToken|refresh_token)"\s*:`)
)

// bulkFromText parses a pasted blob: whole-JSON first, concatenated JSON
// second, then line mode (split on newlines, then on 2+ spaces / tabs). A
// JSON export that yields no token is rejected whole, never shredded into
// line-mode tokens.
func bulkFromText(text string) []jwtEntry {
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		var parsed any
		if err := json.Unmarshal([]byte(t), &parsed); err == nil {
			return bulkToEntries(bulkAccountList(parsed))
		}
		if values := bulkParseConcatenated(t); values != nil {
			var out []jwtEntry
			for _, v := range values {
				out = append(out, bulkToEntries(bulkAccountList(v))...)
			}
			return out
		}
		if bulkTokenFieldRe.MatchString(t) {
			return []jwtEntry{}
		}
	}
	var items []any
	for _, seg := range bulkLineSplitRe.Split(text, -1) {
		for _, piece := range bulkPieceSplitRe.Split(seg, -1) {
			if piece != "" {
				items = append(items, piece)
			}
		}
	}
	return bulkToEntries(items)
}

// bulkExtractEntries dispatches on the decoded request body the way Axon's
// extractTokenEntries does: raw string, {text}, or a list/wrapper/single
// account object.
func bulkExtractEntries(raw any) []jwtEntry {
	switch v := raw.(type) {
	case string:
		return bulkFromText(v)
	case map[string]any:
		if s, ok := v["text"].(string); ok {
			return bulkFromText(s)
		}
		return bulkToEntries(bulkAccountList(v))
	case []any:
		return bulkToEntries(bulkAccountList(v))
	}
	return nil
}

// bulkExistingState reads the provider's stored connections once: the token
// set for dedupe (Axon's apiKey || accessToken || psd.accessToken ||
// data.accessToken chain) and the next free DD-MM-YYYY-<n> name index.
func (h *OAuthHandler) bulkExistingState(provider string) (map[string]bool, int) {
	tokens := map[string]bool{}
	next := 0
	if h.Repo == nil || h.Repo.RawDB() == nil {
		return tokens, next
	}
	now := time.Now()
	prefix := fmt.Sprintf("%02d-%02d-%d", now.Day(), int(now.Month()), now.Year())
	nameRe := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `-(\d+)$`)
	rows, err := h.Repo.RawDB().Query(
		"SELECT name, data FROM providerConnections WHERE provider = ?", provider)
	if err != nil {
		return tokens, next
	}
	defer rows.Close()
	for rows.Next() {
		var name, dataStr sql.NullString
		if err := rows.Scan(&name, &dataStr); err != nil {
			continue
		}
		if name.Valid {
			if m := nameRe.FindStringSubmatch(name.String); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil && n > next {
					next = n
				}
			}
		}
		var blob map[string]any
		if !dataStr.Valid || json.Unmarshal([]byte(dataStr.String), &blob) != nil {
			continue
		}
		psd, _ := blob["providerSpecificData"].(map[string]any)
		inner, _ := blob["data"].(map[string]any)
		for _, cand := range []any{blob["apiKey"], blob["accessToken"], psd["accessToken"], inner["accessToken"]} {
			if s, ok := cand.(string); ok && s != "" {
				tokens[s] = true
				break
			}
		}
	}
	return tokens, next
}

// bulkCreateEntry stores one parsed entry (priority 3, testStatus active):
// JWTs decode for email/expiry/type, a refresh token arms the oauth shape,
// bare tokens keep the apikey shape. Returns the Axon result entry or the
// per-entry failure message.
func (h *OAuthHandler) bulkCreateEntry(provider string, index int, e jwtEntry, nextIndex *int) (map[string]any, string) {
	var payload map[string]any
	if strings.HasPrefix(e.token, "eyJ") {
		payload = decodeJWTPayload(e.token)
		if payload == nil {
			return nil, "Invalid JWT (cannot decode payload)"
		}
	}
	tokenType := "apikey"
	email := ""
	var expiresAt string
	if payload != nil {
		tokenType = "jwt"
		if payload["typ"] == "Offline" {
			tokenType = "offline-jwt"
		}
		email = stringClaim(payload, "email")
		if exp, ok := payload["exp"].(float64); ok { // JSON numbers are always finite
			expMs := exp * 1000
			switch {
			case expMs > 8.64e15:
				// Beyond Date range: Axon stores no expiry.
			case expMs < -8.64e15:
				return nil, "Invalid time value"
			default:
				expiresAt = time.UnixMilli(int64(expMs)).UTC().Format("2006-01-02T15:04:05.000Z")
			}
		}
	}
	name := e.name
	if name == "" {
		*nextIndex++
		now := time.Now()
		name = fmt.Sprintf("%02d-%02d-%d-%d", now.Day(), int(now.Month()), now.Year(), *nextIndex)
	}
	authType := "apikey"
	dataMap := map[string]any{"testStatus": "active"}
	if e.refreshToken != "" {
		authType = "oauth"
		dataMap["accessToken"] = e.token
		dataMap["refreshToken"] = e.refreshToken
	} else {
		dataMap["apiKey"] = e.token
	}
	if email != "" {
		dataMap["email"] = email
	}
	if expiresAt != "" {
		dataMap["expiresAt"] = expiresAt
	}
	prio := 3
	connID := provider + "-" + randomString(12)
	if err := h.saveAxonConnection(connID, provider, authType, name, email, &prio, dataMap); err != nil {
		return nil, err.Error()
	}
	return map[string]any{"index": index, "ok": true, "id": connID, "name": name, "type": tokenType}, ""
}

// dynBulkJWT is Axon's POST bulk-jwt action (codebuddy-cn, codebuddy-intl,
// workbuddy): parse pasted tokens/accounts, dedupe against stored
// connections and within the batch, create serially at priority 3, and
// answer with counts — success is a NUMBER here, not a boolean.
func (h *OAuthHandler) dynBulkJWT(w http.ResponseWriter, provider string, bodyAny any) {
	if provider != "codebuddy-intl" && provider != "codebuddy-cn" && provider != "workbuddy" {
		axonError(w, http.StatusBadRequest, "Provider not supported")
		return
	}
	entries := bulkExtractEntries(bodyAny)
	if len(entries) == 0 {
		axonError(w, http.StatusBadRequest,
			"No tokens found. Paste an account JSON object, or one token per line as name|token.")
		return
	}
	seen, nextIndex := h.bulkExistingState(provider)
	results := []any{}
	success, failed, skipped := 0, 0, 0
	for i, e := range entries {
		if seen[e.token] {
			results = append(results, map[string]any{
				"index": i, "ok": true, "skipped": true, "reason": "duplicate",
			})
			skipped++
			continue
		}
		seen[e.token] = true
		res, failMsg := h.bulkCreateEntry(provider, i, e, &nextIndex)
		if failMsg != "" {
			results = append(results, map[string]any{"index": i, "ok": false, "error": failMsg})
			failed++
			continue
		}
		results = append(results, res)
		success++
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": success, "failed": failed, "skipped": skipped, "results": results,
	})
}
