package dashboard

import (
	"encoding/base64"
	json "encoding/json/v2"
	"strings"
)

// CodeBuddy-family Keycloak userinfo probe URL (fragment 977), porting
// the buildUrl of upstream's testUtils.js entries for codebuddy-intl
// and workbuddy: the realm base is the token JWT's `iss` claim when it
// is an https URL, with any trailing slash trimmed; a token without a
// usable iss falls back to the brand's public realm. The probe itself
// (GET, Bearer, refresh-on-401) rides the generic machinery in
// connection_probe.go via the providers' oauthProbeConfigs entries.

// codebuddyUserinfoURL derives the userinfo endpoint for one stored
// access token.
func codebuddyUserinfoURL(token, fallbackRealm string) string {
	realm := fallbackRealm
	if iss := jwtIssuerClaim(token); strings.HasPrefix(iss, "https://") {
		realm = strings.TrimSuffix(iss, "/")
	}
	return realm + "/protocol/openid-connect/userinfo"
}

// jwtIssuerClaim reads the `iss` claim out of a JWT payload. It mirrors
// the package's other claim readers (jwtEmailClaim, jwtSubject):
// anything malformed yields "".
func jwtIssuerClaim(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Iss string `json:"iss"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Iss
}
