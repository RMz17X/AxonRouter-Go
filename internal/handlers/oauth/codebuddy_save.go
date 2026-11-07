package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"axonrouter/proxy/internal/db"
)

// CodeBuddy-family device-save identity (fragment 975).
//
// Upstream keys a re-login to the account, not the credential: the save
// path merges by provider + email (src/lib/db/repos/connectionsRepo.js),
// so a rotated access token UPDATES the existing connection row instead
// of piling up a duplicate keyed by the new token's hash. The helpers
// here give the family's device polls and saveDeviceConnection that
// behavior, following the zcode email-anchor precedent in this package.

// codebuddyDefaultExpiresIn is upstream's fallback token lifetime for
// the family: mapTokens stores expires_in || 86400 for all three
// providers, so a poll that omits expires_in still yields an expiresAt.
const codebuddyDefaultExpiresIn = 86400

func isCodebuddyFamilyProvider(provider string) bool {
	switch provider {
	case "codebuddy-cn", "codebuddy-intl", "workbuddy":
		return true
	default:
		return false
	}
}

// codebuddyExistingIdentity finds the active connection row already
// stored for this family account (provider + email) and returns its id,
// or "" when none exists. Same scan shape as zcodeExistingIdentity: the
// email lives inside the row's data JSON, so rows are matched after a
// light parse rather than by a column the device path does not write.
func codebuddyExistingIdentity(ctx context.Context, repo *db.Repo, provider, email string) string {
	email = strings.TrimSpace(email)
	if repo == nil || email == "" || provider == "" {
		return ""
	}
	rows, err := repo.QueryContext(ctx,
		"SELECT id, data FROM providerConnections WHERE provider = ? AND isActive = 1 ORDER BY updatedAt DESC", provider)
	if err != nil {
		return ""
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, data string
		if rows.Scan(&id, &data) != nil {
			continue
		}
		var parsed struct {
			Email string `json:"email"`
		}
		if json.Unmarshal([]byte(data), &parsed) != nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(parsed.Email), email) {
			return id
		}
	}
	if err := rows.Err(); err != nil {
		return ""
	}
	return ""
}

// codebuddyJWTClaims decodes a JWT payload for the family's identity
// extraction. Returns nil for a non-JWT credential.
func codebuddyJWTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload := parts[1]
	if l := len(payload) % 4; l > 0 {
		payload += strings.Repeat("=", 4-l)
	}
	data, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		data, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	var claims map[string]any
	if json.Unmarshal(data, &claims) != nil {
		return nil
	}
	return claims
}

// codebuddyEmailFromClaims follows upstream's extractEmail precedence
// for the family (src/lib/oauth/utils.js): email, then
// preferred_username, then sub — with no "@" requirement, because the
// plugin tokens carry the account id in sub on some brands.
func codebuddyEmailFromClaims(claims map[string]any) string {
	for _, k := range []string{"email", "preferred_username", "sub"} {
		if v, ok := claims[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// codebuddyDisplayNameFromClaims mirrors upstream's extractDisplayName
// (providerHelpers.js), used by codebuddy-intl's mapTokens: the name
// claim, then given + family name, then the email local part.
func codebuddyDisplayNameFromClaims(claims map[string]any, email string) string {
	if v, ok := claims["name"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	given, _ := claims["given_name"].(string)
	family, _ := claims["family_name"].(string)
	if joined := strings.TrimSpace(strings.TrimSpace(given) + " " + strings.TrimSpace(family)); joined != "" {
		return joined
	}
	if i := strings.Index(email, "@"); i > 0 {
		return email[:i]
	}
	return ""
}
