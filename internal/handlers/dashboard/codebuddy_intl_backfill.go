package dashboard

import (
	"encoding/base64"
	json "encoding/json/v2"
	"regexp"
	"strings"
	"sync"

	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/models"
)

// CodeBuddy Intl identity backfill (fragment 977), porting upstream's
// backfillCodeBuddyIntlIdentity (src/lib/oauth/providers/index.js).
//
// Connections saved before the family's identity extraction existed
// (or imported without it) can carry an access token but no email /
// displayName, and a generic "Account N" display name. Upstream repairs
// them once per process from the providers list routes — the JWT
// already names the account, so the identity is recovered from the
// token instead of asking the operator to re-login. The hook is
// HandleGetProvidersClient, which serves the same two routes
// (GET /api/providers, GET /api/providers/client).
//
// Idempotency: the scan only patches rows that still miss identity or
// still carry a generic name, and the write fills only empty fields
// (db.SetConnectionCodebuddyIntlIdentity), so a completed pass leaves
// nothing for a second pass to do. The run-once guard mirrors
// upstream's module flag, including its reset-on-failure: a pass that
// could not read or write stays pending and the next list call retries
// it. WorkBuddy and CN have no upstream backfill — none is invented
// for them here.

// codebuddyIntlBackfillGuard is the process-wide run-once state.
var codebuddyIntlBackfillGuard struct {
	sync.Mutex
	done bool
}

// genericAccountNameRe is upstream's /^Account \d+$/ rename trigger.
var genericAccountNameRe = regexp.MustCompile(`^Account \d+$`)

// backfillCodebuddyIntlIdentity runs the once-per-process repair. It
// never fails the list call that triggered it: errors are logged and
// the pass stays pending for the next call.
func (h *DashboardHandler) backfillCodebuddyIntlIdentity() {
	if h == nil || h.Repo == nil {
		return
	}
	codebuddyIntlBackfillGuard.Lock()
	defer codebuddyIntlBackfillGuard.Unlock()
	if codebuddyIntlBackfillGuard.done {
		return
	}
	conns, err := h.Repo.GetProviderConnections("codebuddy-intl", false)
	if err != nil {
		log.Warn("usage", "codebuddy-intl identity backfill scan failed", "error", err)
		return
	}
	failed := false
	for _, conn := range conns {
		if conn == nil {
			continue
		}
		if err := backfillCodebuddyIntlConnection(h, conn); err != nil {
			log.Warn("usage", "codebuddy-intl identity backfill failed", "conn", conn.ID, "error", err)
			failed = true
		}
	}
	if !failed {
		codebuddyIntlBackfillGuard.done = true
	}
}

// backfillCodebuddyIntlConnection patches one connection when it is in
// the backfill's target set: codebuddy-intl, oauth, holding an access
// token, and missing an email or displayName or still named "Account N".
func backfillCodebuddyIntlConnection(h *DashboardHandler, conn *models.ProviderConnection) error {
	if conn.AuthType != "oauth" {
		return nil
	}
	var data struct {
		AccessToken string `json:"accessToken"`
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
	}
	if conn.Data != "" {
		_ = json.Unmarshal([]byte(conn.Data), &data)
	}
	if data.AccessToken == "" {
		return nil
	}
	email := data.Email
	if email == "" && conn.Email != nil {
		email = *conn.Email
	}
	name := ""
	if conn.Name != nil {
		name = *conn.Name
	}
	genericName := genericAccountNameRe.MatchString(name)
	if email != "" && data.DisplayName != "" && !genericName {
		return nil
	}
	claims := codebuddyIntlJWTClaims(data.AccessToken)
	if claims == nil {
		return nil
	}
	jwtEmail := codebuddyIntlClaimEmail(claims)
	jwtDisplayName := codebuddyIntlClaimDisplayName(claims, jwtEmail)

	patchEmail := ""
	if email == "" && jwtEmail != "" {
		patchEmail = jwtEmail
	}
	patchDisplayName := ""
	if data.DisplayName == "" && jwtDisplayName != "" {
		patchDisplayName = jwtDisplayName
	}
	renameTo := ""
	if genericName && (jwtEmail != "" || jwtDisplayName != "") {
		renameTo = jwtEmail
		if renameTo == "" {
			renameTo = jwtDisplayName
		}
	}
	if patchEmail == "" && patchDisplayName == "" && renameTo == "" {
		return nil
	}
	return h.Repo.SetConnectionCodebuddyIntlIdentity(conn.ID, patchEmail, patchDisplayName, renameTo)
}

// codebuddyIntlJWTClaims decodes a JWT payload for the backfill's
// identity extraction. Returns nil for a non-JWT credential. The
// dashboard package keeps its claim readers per feature (jwtEmailClaim,
// jwtSubject); this one follows the same shape.
func codebuddyIntlJWTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload := parts[1]
	if l := len(payload) % 4; l > 0 {
		payload += strings.Repeat("=", 4-l)
	}
	raw, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil
	}
	return claims
}

// codebuddyIntlClaimEmail follows upstream's extractEmail precedence
// for the family: email, then preferred_username, then sub — the same
// order the device-save path uses (handlers/oauth/codebuddy_save.go).
func codebuddyIntlClaimEmail(claims map[string]any) string {
	for _, k := range []string{"email", "preferred_username", "sub"} {
		if v, ok := claims[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// codebuddyIntlClaimDisplayName mirrors upstream's extractDisplayName:
// the name claim, then given + family name, then the email local part.
func codebuddyIntlClaimDisplayName(claims map[string]any, email string) string {
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
