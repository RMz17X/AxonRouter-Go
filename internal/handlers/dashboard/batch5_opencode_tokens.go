package dashboard

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"axonrouter/proxy/internal/clisetup"
)

// OpenCode sync tokens. Port of Axon's src/lib/opencodeSync/tokens.ts:
// ocs_ bearer tokens, sha256-hashed at rest, timing-safe verify.

func ocNowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func ocUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func ocHashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ocNormalizeMetadata keeps only scalar values, keys sorted, nulls
// dropped — normalizeMetadata in tokens.ts.
func ocNormalizeMetadata(v any) *clisetup.JObj {
	out := clisetup.NewJObj()
	obj, ok := ocPlain(v)
	if !ok {
		return out
	}
	keys := obj.Keys()
	ocSortStrings(keys)
	for _, k := range keys {
		switch val := obj.Get(k).(type) {
		case string:
			out.Set(k, val)
		case bool:
			out.Set(k, val)
		case stdjson.Number:
			out.Set(k, val)
		case float64:
			out.Set(k, val)
		}
	}
	return out
}

// ocCreateSyncToken mints a token + its stored record. The name rule
// error intentionally matches the JS message: the routes classify it
// as a 400 via their /required/ test.
func ocCreateSyncToken(name, metadata any) (string, *clisetup.JObj, error) {
	trimmed := ocTrim(name)
	if trimmed == "" {
		return "", nil, errors.New("Token name is required")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := "ocs_" + base64.RawURLEncoding.EncodeToString(raw)
	now := ocNowISO()
	record := clisetup.NewJObj(
		"id", ocUUID(),
		"name", trimmed,
		"metadata", ocNormalizeMetadata(metadata),
		"tokenHash", ocHashToken(token),
		"createdAt", now,
		"updatedAt", now,
		"lastUsedAt", nil,
	)
	return token, record, nil
}

// ocVerifySyncToken is the timing-safe hash comparison.
func ocVerifySyncToken(token string, record *clisetup.JObj) bool {
	trimmed := strings.TrimSpace(token)
	hash := ocTrim(record.Get("tokenHash"))
	if trimmed == "" || hash == "" {
		return false
	}
	expected, err := hex.DecodeString(hash)
	if err != nil {
		return false
	}
	actual, err := hex.DecodeString(ocHashToken(trimmed))
	if err != nil || len(expected) != len(actual) {
		return false
	}
	return subtle.ConstantTimeCompare(expected, actual) == 1
}

var ocBearerRE = regexp.MustCompile(`(?i)^Bearer\s+(.+)$`)

// ocExtractBearerToken ports extractBearerToken.
func ocExtractBearerToken(header string) string {
	value := strings.TrimSpace(header)
	if value == "" {
		return ""
	}
	m := ocBearerRE.FindStringSubmatch(value)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// ocFindMatchingToken returns the first record whose hash matches
// the Authorization bearer token, or nil.
func ocFindMatchingToken(records []*clisetup.JObj, authHeader string) *clisetup.JObj {
	token := ocExtractBearerToken(authHeader)
	if token == "" {
		return nil
	}
	for _, rec := range records {
		if ocVerifySyncToken(token, rec) {
			return rec
		}
	}
	return nil
}

// ocPublicTokenRecord ports toPublicTokenRecord (hash never leaves).
func ocPublicTokenRecord(record *clisetup.JObj) *clisetup.JObj {
	if record == nil {
		return nil
	}
	lastUsed := record.Get("lastUsedAt")
	if s, ok := lastUsed.(string); ok && s == "" {
		lastUsed = nil
	}
	return clisetup.NewJObj(
		"id", record.Get("id"),
		"name", record.Get("name"),
		"metadata", ocNormalizeMetadata(record.Get("metadata")),
		"createdAt", record.Get("createdAt"),
		"updatedAt", record.Get("updatedAt"),
		"lastUsedAt", lastUsed,
	)
}

// ocNormalizeTokenPatch ports normalizeSyncTokenPatch. Errors carry
// the JS messages; the route classifies them as 400s.
func ocNormalizeTokenPatch(input any) (*clisetup.JObj, error) {
	obj, ok := ocPlain(input)
	if !ok {
		return nil, errors.New("Invalid token payload")
	}
	updates := clisetup.NewJObj()
	if _, present := hasKey(obj, "name"); present {
		name := ocTrim(obj.Get("name"))
		if name == "" {
			return nil, errors.New("Token name is required")
		}
		updates.Set("name", name)
	}
	if _, present := hasKey(obj, "metadata"); present {
		if !ocIsPlain(obj.Get("metadata")) {
			return nil, errors.New("Invalid token metadata")
		}
		updates.Set("metadata", ocNormalizeMetadata(obj.Get("metadata")))
	}
	return updates, nil
}

func hasKey(obj *clisetup.JObj, key string) (any, bool) {
	for _, k := range obj.Keys() {
		if k == key {
			return obj.Get(k), true
		}
	}
	return nil, false
}
