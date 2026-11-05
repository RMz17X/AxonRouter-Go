package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/auth"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/middleware"
)

// Dashboard auth surface (batch 7, AxonRouter parity): GET
// /api/auth/session, POST /api/auth/reset-password, and the
// requireManagementAuth gate the credentials + MCP audit routes share.
//
// Error bodies here are Axon's flat { "error": "..." } shape, written with
// WriteJSON directly — handlerutil.WriteJSONError would wrap them in the
// OpenAI-style envelope the data plane uses.

// HandleAuthSession serves GET /api/auth/session: report whether the
// auth_token cookie carries a currently valid dashboard session, with the
// upstream reason vocabulary (missing_cookie / valid_paseto / expired_token
// / invalid_signature / invalid_token). Go sessions are HS256 JWTs, not
// PASETO tokens; the reason strings stay upstream's so existing clients
// keep parsing them (see .changes/918).
func HandleAuthSession(w http.ResponseWriter, r *http.Request) {
	base := map[string]any{
		"host":           sessionHost(r),
		"forwardedProto": r.Header.Get("X-Forwarded-Proto"),
		"secureRequest":  r.TLS != nil,
	}

	token := authTokenCookieValue(r)
	if token == "" {
		base["authenticated"] = false
		base["reason"] = "missing_cookie"
		handlerutil.WriteJSON(w, http.StatusOK, base)
		return
	}

	payload, reason := classifySessionToken(token)
	if payload == nil {
		base["authenticated"] = false
		base["reason"] = reason
		handlerutil.WriteJSON(w, http.StatusOK, base)
		return
	}
	base["authenticated"] = true
	base["reason"] = "valid_paseto"
	base["payload"] = payload
	handlerutil.WriteJSON(w, http.StatusOK, base)
}

// sessionHost mirrors the session route's host field: the Host header,
// falling back to the request URL's host.
func sessionHost(r *http.Request) string {
	if r.Host != "" {
		return r.Host
	}
	if r.URL != nil {
		return r.URL.Host
	}
	return ""
}

// authTokenCookieValue extracts the auth_token cookie by scanning the raw
// Cookie header, the way the upstream route does (first matching part,
// value taken verbatim).
func authTokenCookieValue(r *http.Request) string {
	header := r.Header.Get("Cookie")
	if header == "" {
		return ""
	}
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "auth_token=") {
			return strings.TrimPrefix(part, "auth_token=")
		}
	}
	return ""
}

// classifySessionToken verifies a session JWT step by step so the failure
// reason can be told apart the way upstream's PASETO error classification
// does: structural problems are invalid_token, an HMAC mismatch is
// invalid_signature, and a well-signed but expired token is expired_token.
// Returns the claims payload on success.
func classifySessionToken(token string) (map[string]any, string) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, "invalid_token"
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, "invalid_token"
	}
	secret := auth.Secret()
	if secret == "" {
		return nil, "invalid_token"
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(mac.Sum(nil), sig) {
		return nil, "invalid_signature"
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, "invalid_token"
	}
	var claims auth.SessionClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, "invalid_token"
	}
	if !claims.Authenticated {
		return nil, "invalid_token"
	}
	if claims.Exp > 0 && time.Now().Unix() > claims.Exp {
		return nil, "expired_token"
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, "invalid_token"
	}
	return payload, ""
}

// HandleAuthResetPassword serves POST /api/auth/reset-password: clear the
// stored dashboard password hash so login falls back to the default
// password. Upstream guards this route as local-only (dashboardGuard
// LOCAL_ONLY_PATHS); the gate below mirrors canAccessLocalOnlyRoute.
func HandleAuthResetPassword(repo *db.Repo) http.HandlerFunc {
	return RequireLocalOnlyRoute(repo, func(w http.ResponseWriter, r *http.Request) {
		if err := repo.UpdateSettingsRaw(map[string]any{"password": nil}); err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
			})
			return
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
	})
}

// RequireLocalOnlyRoute wraps a handler with dashboardGuard's
// LOCAL_ONLY gate: the caller must hold the CLI token, or arrive
// directly from a private/loopback peer while authenticated
// (canAccessLocalOnlyRoute). Axon applies the check to five paths
// before routing; in Go it composes at registration, behind the
// dashboard gate — the same in-handler composition the
// reset-password port established. Denial is Axon's flat 403 body.
// The paths are the three OAuth auto-imports and cowork-settings
// (council round 2, F-B2); reset-password above shares the wrapper.
func RequireLocalOnlyRoute(repo *db.Repo, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !canAccessLocalOnlyRoute(repo, r) {
			handlerutil.WriteJSON(w, http.StatusForbidden, map[string]any{
				"error": "Local only: CLI token required",
			})
			return
		}
		next(w, r)
	}
}

// canAccessLocalOnlyRoute mirrors dashboardGuard.canAccessLocalOnlyRoute:
// a valid CLI token always passes; otherwise the request must arrive
// directly from a private/loopback peer (not via a proxy) AND be
// authenticated (valid session, or login not required).
func canAccessLocalOnlyRoute(repo *db.Repo, r *http.Request) bool {
	if auth.ValidCLIToken(r.Header.Get(auth.CLITokenHeader)) {
		return true
	}
	if !isDirectPrivateRequest(r) {
		return false
	}
	return auth.SessionValid(r) || !auth.RequireLogin(repo)
}

// isDirectPrivateRequest mirrors dashboardGuard.isDirectPrivateRequest.
// Upstream reads the peer from the x-axonrouter-real-ip header, which its
// outer server stamps from the TCP socket after proving itself with a
// per-boot secret. The Go server terminates the connection itself, so the
// socket peer (RemoteAddr) is the same evidence — the trusted-peer header
// path is honoured too when the deployment does stamp it.
func isDirectPrivateRequest(r *http.Request) bool {
	if r.Header.Get("x-axonrouter-via-proxy") != "" {
		return false
	}
	peer := ""
	if token := os.Getenv("AXONROUTER_PEER_TOKEN"); token != "" &&
		subtle.ConstantTimeCompare([]byte(r.Header.Get("x-axonrouter-peer-token")), []byte(token)) == 1 {
		peer = r.Header.Get("x-axonrouter-real-ip")
	} else {
		peer = remoteAddrHost(r)
	}
	if !isPrivateNetworkHostname(peer) {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || !isPrivateNetworkHostname(parsed.Hostname()) {
			return false
		}
	}
	return true
}

// remoteAddrHost returns the host part of the request's remote address.
func remoteAddrHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isPrivateNetworkHostname ports dashboardGuard.isPrivateNetworkHostname:
// loopback names, *.local / *.lan / *.internal, and the private + CGNAT +
// link-local IPv4 ranges. Accepts a host with an optional port, a bracketed
// IPv6 literal, or a bare IP.
func isPrivateNetworkHostname(h string) bool {
	if h == "" {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(h))
	if strings.HasPrefix(name, "[") {
		end := strings.Index(name, "]")
		if end == -1 {
			return false
		}
		name = name[1:end]
	} else if strings.Count(name, ":") == 1 {
		name = name[:strings.Index(name, ":")]
	}
	name = strings.TrimPrefix(name, "::ffff:")
	switch name {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	if strings.HasSuffix(name, ".local") || strings.HasSuffix(name, ".lan") || strings.HasSuffix(name, ".internal") {
		return true
	}
	parts := strings.Split(name, ".")
	if len(parts) != 4 {
		return false
	}
	nums := make([]int, 4)
	for i, p := range parts {
		if len(p) == 0 || len(p) > 3 {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
		n, err := strconv.Atoi(p)
		if err != nil || n > 255 {
			return false
		}
		nums[i] = n
	}
	switch {
	case nums[0] == 127:
		return true
	case nums[0] == 10:
		return true
	case nums[0] == 172 && nums[1] >= 16 && nums[1] <= 31:
		return true
	case nums[0] == 192 && nums[1] == 168:
		return true
	case nums[0] == 100 && nums[1] >= 64 && nums[1] <= 127:
		return true
	case nums[0] == 169 && nums[1] == 254:
		return true
	}
	return false
}

// RequireOperatorCaller wraps a handler with the operator-class check:
// the caller must be dashboard-privileged (a session, the local CLI
// token, or an install with login disabled) — the CallerIsDashboardPrivileged
// idiom the F-6 fix established for the API-key list. It exists because
// the gates these routes already sit behind compose badly (council
// round 1, S-1): the dashboard group admits any engine API key, and
// RequireManagementAuth admits any whitelisted source address — the
// default whitelist covers the whole LAN — so an engine key held by a
// client app on any LAN machine could export or replace every provider
// credential. An engine key is the lowest-trust principal in the
// system; routes that return or accept credential material (and the
// MCP audit pair sharing their gate) require an operator credential
// in addition. Failure answers the same flat 401 as
// RequireManagementAuth.
func RequireOperatorCaller(repo *db.Repo, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !middleware.CallerIsDashboardPrivileged(repo, r) {
			handlerutil.WriteJSON(w, http.StatusUnauthorized, map[string]any{
				"error": "Unauthorized",
			})
			return
		}
		next(w, r)
	}
}

// RequireOperatorForWrites is the dashboard groups' write sweep
// (council round 3, Fix 1). Rounds 1–2 gated the secret-bearing
// surfaces one route at a time; the round-3 census proved the
// dashboard group's ENTIRE state-changing surface still admitted any
// active engine API key — key minting/rotation-theft/deletion,
// plaintext provider-credential export's write siblings, connection
// redirection to an attacker upstream, tunnel enable, guardrail
// policy deletion, sync-token minting. The sweep inverts the
// default: every POST/PUT/PATCH/DELETE in the dashboard groups must
// come from an operator-class caller (session, CLI token, or
// login-disabled install — CallerIsDashboardPrivileged), except the
// routes exempt() names, each with its reason recorded at the call
// site. GET/HEAD keep the classes rounds 1–2 aligned. An engine key
// remains what it is for: spending on the data plane (/v1, MCP
// transports), never administering the gateway.
func RequireOperatorForWrites(repo *db.Repo, exempt func(method, path string) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			default:
				next.ServeHTTP(w, r)
				return
			}
			if exempt != nil && exempt(r.Method, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			if !middleware.CallerIsDashboardPrivileged(repo, r) {
				handlerutil.WriteJSON(w, http.StatusUnauthorized, map[string]any{
					"error": "Unauthorized",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireManagementAuth wraps a handler with the port of
// requireManagementAuth (used by /api/credentials/* and /api/mcp/audit*):
// the request must come from a whitelisted local address or carry a valid
// dashboard session. Failure answers 401 { "error": "Unauthorized" }.
func RequireManagementAuth(repo *db.Repo, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !managementAuthOK(repo, r) {
			handlerutil.WriteJSON(w, http.StatusUnauthorized, map[string]any{
				"error": "Unauthorized",
			})
			return
		}
		next(w, r)
	}
}

// managementAuthOK is the boolean core of RequireManagementAuth. Upstream
// also writes a management_auth_attempt audit entry when
// settings.auditLogEnabled is on; the Go codebase has no management audit
// store, so that side effect is not ported (see .changes/918).
func managementAuthOK(repo *db.Repo, r *http.Request) bool {
	settings, _ := repo.GetSettingsRaw()
	if isWhitelistedLocalRequest(settings, r) {
		return true
	}
	return auth.SessionValid(r)
}

// isWhitelistedLocalRequest ports ipValidator.isLocalRequest: the client
// IP (per getClientIP's priority rules) must match settings.ipWhitelist
// exactly or by IPv4 CIDR. The default whitelist is upstream's.
func isWhitelistedLocalRequest(settings map[string]any, r *http.Request) bool {
	clientIP := managementClientIP(settings, r)
	if clientIP == "" {
		return false
	}
	whitelist := []string{"127.0.0.1", "::1", "172.17.0.0/16", "192.168.0.0/16"}
	if raw, ok := settings["ipWhitelist"]; ok {
		if list, ok := raw.([]any); ok {
			whitelist = make([]string, 0, len(list))
			for _, entry := range list {
				if s, ok := entry.(string); ok {
					whitelist = append(whitelist, s)
				}
			}
		}
	}
	for _, entry := range whitelist {
		if entry == clientIP {
			return true
		}
		if strings.Contains(entry, "/") && ipv4CIDRMatch(clientIP, entry) {
			return true
		}
	}
	return false
}

// managementClientIP ports ipValidator.getClientIP: a trusted proxy's
// X-Forwarded-For wins when settings.trustedProxyEnabled is on, then the
// socket address, then X-Real-IP. IPv4-mapped IPv6 prefixes are stripped.
func managementClientIP(settings map[string]any, r *http.Request) string {
	socketIP := remoteAddrHost(r)
	if enabled, _ := settings["trustedProxyEnabled"].(bool); enabled {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return normalizeIP(strings.TrimSpace(strings.Split(xff, ",")[0]))
		}
	}
	if socketIP != "" {
		return normalizeIP(socketIP)
	}
	if realIP := r.Header.Get("X-Real-IP"); realIP != "" {
		return normalizeIP(realIP)
	}
	return ""
}

// normalizeIP strips the IPv4-mapped IPv6 prefix, as ipValidator does.
func normalizeIP(ip string) string {
	return strings.TrimPrefix(ip, "::ffff:")
}

// ipv4CIDRMatch ports ipValidator.cidrMatch (IPv4 only, like upstream).
func ipv4CIDRMatch(ip, cidr string) bool {
	rangePart, bitsPart, found := strings.Cut(cidr, "/")
	if !found {
		return false
	}
	bits, err := strconv.Atoi(bitsPart)
	if err != nil || bits < 0 || bits > 32 {
		return false
	}
	ipInt, okIP := ipv4ToInt(ip)
	rangeInt, okRange := ipv4ToInt(rangePart)
	if !okIP || !okRange {
		return false
	}
	var mask uint32
	if bits == 0 {
		mask = 0
	} else {
		mask = ^uint32(0) << (32 - bits)
	}
	return ipInt&mask == rangeInt&mask
}

// ipv4ToInt parses a dotted-quad IPv4 address into a uint32.
func ipv4ToInt(ip string) (uint32, bool) {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return 0, false
	}
	var out uint32
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return 0, false
		}
		out = out<<8 | uint32(n)
	}
	return out, true
}
