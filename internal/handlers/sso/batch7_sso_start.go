package sso

import (
	"bytes"
	"compress/flate"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
)

// SSO login initiators (batch 7, AxonRouter parity).
//
// GET /api/auth/oidc/start and GET /api/auth/saml/start are the missing
// first leg of the SSO flows whose callbacks (/api/auth/oidc/callback,
// /api/auth/saml/acs) the dashboard login already completes. Both handlers
// follow the upstream routes exactly: configuration gaps and runtime
// failures alike answer with a 307 redirect to /login?error=<code>, never
// a JSON error.

// HandleOidcStart handles GET /api/auth/oidc/start: build the IdP
// authorization URL (state + nonce + PKCE S256), stash the three flow
// cookies, and redirect to the IdP.
func (h *Handler) HandleOidcStart(w http.ResponseWriter, r *http.Request) {
	origin := publicOrigin(r)
	settings, _ := h.Repo.GetSettingsRaw()
	if settings == nil {
		settings = map[string]any{}
	}

	issuerURL, clientID, scopes, configured := oidcRuntimeConfig(settings)
	if !configured {
		http.Redirect(w, r, origin+"/login?error=oidc_not_configured", http.StatusTemporaryRedirect)
		return
	}

	discovery, err := fetchDiscovery(issuerURL)
	if err != nil {
		redirectLoginError(w, r, origin, err.Error(), "oidc_start_failed")
		return
	}

	state := randomBase64URL(16)
	nonce := randomBase64URL(16)
	verifier := randomBase64URL(32)
	challengeSum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeSum[:])

	authURL := buildAuthorizationURL(
		discovery.AuthorizationEndpoint,
		"response_type", "code",
		"client_id", clientID,
		"redirect_uri", origin+oidcCallback,
		"scope", scopes,
		"state", state,
		"nonce", nonce,
		"code_challenge", challenge,
		"code_challenge_method", "S256",
	)

	setSSOFlowCookie(w, r, "oidc_state", state)
	setSSOFlowCookie(w, r, "oidc_nonce", nonce)
	setSSOFlowCookie(w, r, "oidc_code_verifier", verifier)
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

// HandleSamlStart handles GET /api/auth/saml/start: build an AuthnRequest,
// deflate + base64 it onto the IdP entry point as SAMLRequest, stash the
// request id in the saml_state cookie, and redirect.
func (h *Handler) HandleSamlStart(w http.ResponseWriter, r *http.Request) {
	settings, _ := h.Repo.GetSettingsRaw()
	if settings == nil {
		settings = map[string]any{}
	}
	origin := samlBaseURL(r, settings)

	entryPoint := handlerutil.GetString(settings, "samlEntryPoint")
	cert := handlerutil.GetString(settings, "samlCert")
	if entryPoint == "" || cert == "" {
		http.Redirect(w, r, origin+"/login?error=saml_not_configured", http.StatusTemporaryRedirect)
		return
	}

	authorizeURL, requestID, err := buildSAMLAuthorizeURL(settings, origin)
	if err != nil {
		redirectLoginError(w, r, origin, err.Error(), "saml_start_failed")
		return
	}

	setSSOFlowCookie(w, r, "saml_state", requestID)
	http.Redirect(w, r, authorizeURL, http.StatusTemporaryRedirect)
}

// oidcRuntimeConfig mirrors getOidcRuntimeConfig: SSO is only live when the
// auth mode includes OIDC and issuer/client id/client secret are all set.
func oidcRuntimeConfig(settings map[string]any) (issuerURL, clientID, scopes string, configured bool) {
	authMode := handlerutil.GetString(settings, "authMode")
	if authMode != "oidc" && authMode != "both" {
		return "", "", "", false
	}
	issuerURL = strings.TrimRight(strings.TrimSpace(handlerutil.GetString(settings, "oidcIssuerUrl")), "/")
	clientID = strings.TrimSpace(handlerutil.GetString(settings, "oidcClientId"))
	clientSecret := strings.TrimSpace(handlerutil.GetString(settings, "oidcClientSecret"))
	if issuerURL == "" || clientID == "" || clientSecret == "" {
		return "", "", "", false
	}
	scopes = strings.TrimSpace(handlerutil.GetString(settings, "oidcScopes"))
	if scopes == "" {
		scopes = defaultScopes
	}
	return issuerURL, clientID, scopes, true
}

// samlBaseURL mirrors getSamlBaseUrl: the settings base URL wins over the
// env BASE_URL, then forwarded host headers, then the request origin, then
// the historical localhost default.
func samlBaseURL(r *http.Request, settings map[string]any) string {
	if base := strings.TrimSpace(handlerutil.GetString(settings, "baseUrl")); base != "" {
		return strings.TrimRight(base, "/")
	}
	if base := strings.TrimSpace(os.Getenv("BASE_URL")); base != "" {
		return strings.TrimRight(base, "/")
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	if host != "" {
		proto := strings.TrimSuffix(r.Header.Get("X-Forwarded-Proto"), ":")
		if proto == "" {
			proto = "http"
			if r.TLS != nil {
				proto = "https"
			}
		}
		return strings.TrimRight(proto+"://"+host, "/")
	}
	return "http://localhost:3777"
}

// buildSAMLAuthorizeURL renders the AuthnRequest and the redirect URL the
// way @node-saml/node-saml does: raw-deflate + base64 of the XML in the
// SAMLRequest query parameter of the entry point. No SP private key is
// configured in this product, so the request is unsigned, as upstream.
func buildSAMLAuthorizeURL(settings map[string]any, origin string) (string, string, error) {
	entryPoint := handlerutil.GetString(settings, "samlEntryPoint")
	issuer := handlerutil.GetString(settings, "samlIssuer")
	if issuer == "" {
		// The Go SP metadata + test endpoints already default to this
		// issuer; the start route uses the same value so the AuthnRequest
		// and the metadata document agree. (Upstream defaults to
		// "urn:axonrouter:sp" — recorded divergence, see .changes/918.)
		issuer = "urn:9router:sp"
	}
	acsURL := origin + samlACSPath

	requestID := "_" + randomHex(20)
	issueInstant := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	xml := fmt.Sprintf(`<samlp:AuthnRequest xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="%s" Version="2.0" IssueInstant="%s" Destination="%s" ProtocolBinding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST" AssertionConsumerServiceURL="%s"><saml:Issuer>%s</saml:Issuer><samlp:NameIDPolicy Format="urn:oasis:names:tc:SAML:1.1:nameid-format:unspecified" AllowCreate="true"/></samlp:AuthnRequest>`,
		xmlEscape(requestID), issueInstant, xmlEscape(entryPoint), xmlEscape(acsURL), xmlEscape(issuer))

	var buf bytes.Buffer
	deflater, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		return "", "", err
	}
	if _, err := deflater.Write([]byte(xml)); err != nil {
		return "", "", err
	}
	if err := deflater.Close(); err != nil {
		return "", "", err
	}
	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())

	target, err := url.Parse(entryPoint)
	if err != nil {
		return "", "", fmt.Errorf("Invalid URL")
	}
	sep := "?"
	if target.RawQuery != "" || strings.Contains(entryPoint, "?") {
		sep = "&"
	}
	return entryPoint + sep + "SAMLRequest=" + encodeURIComponent(encoded), requestID, nil
}

// buildAuthorizationURL appends query parameters in the exact order given,
// the way URLSearchParams preserves insertion order upstream.
func buildAuthorizationURL(endpoint string, pairs ...string) string {
	var b strings.Builder
	b.WriteString(endpoint)
	sep := "?"
	if strings.Contains(endpoint, "?") {
		sep = "&"
	}
	for i := 0; i+1 < len(pairs); i += 2 {
		b.WriteString(sep)
		sep = "&"
		b.WriteString(formEncode(pairs[i]))
		b.WriteString("=")
		b.WriteString(formEncode(pairs[i+1]))
	}
	return b.String()
}

// setSSOFlowCookie writes one short-lived flow cookie with the attributes
// both upstream start routes use.
func setSSOFlowCookie(w http.ResponseWriter, r *http.Request, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   shouldUseSecureCookie(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// shouldUseSecureCookie mirrors dashboardSession.shouldUseSecureCookie.
func shouldUseSecureCookie(r *http.Request) bool {
	return os.Getenv("AUTH_COOKIE_SECURE") == "true" || r.Header.Get("X-Forwarded-Proto") == "https"
}

// redirectLoginError answers with /login?error=<message>, URI-component
// encoded, as both upstream start routes do on failure.
func redirectLoginError(w http.ResponseWriter, r *http.Request, origin, message, fallback string) {
	if message == "" {
		message = fallback
	}
	http.Redirect(w, r, origin+"/login?error="+encodeURIComponent(message), http.StatusTemporaryRedirect)
}

// randomBase64URL returns n random bytes in base64url without padding.
func randomBase64URL(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is not expected; fall back to time-derived
		// bytes rather than panicking in a handler.
		for i := range buf {
			buf[i] = byte(time.Now().UnixNano() >> (i % 8))
		}
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// randomHex returns n random bytes hex-encoded.
func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		for i := range buf {
			buf[i] = byte(time.Now().UnixNano() >> (i % 8))
		}
	}
	return fmt.Sprintf("%x", buf)
}

// formEncode is application/x-www-form-urlencoded encoding as
// URLSearchParams applies it (space as "+", the JS unreserved set kept).
func formEncode(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			c == '*' || c == '-' || c == '.' || c == '_':
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// encodeURIComponent mirrors JS encodeURIComponent.
func encodeURIComponent(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			c == '-' || c == '_' || c == '.' || c == '!' || c == '~' ||
			c == '*' || c == '\'' || c == '(' || c == ')':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
