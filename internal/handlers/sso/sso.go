// Package sso implements the dashboard Single Sign-On settings endpoints that
// the profile page calls: the SAML configuration check and SP metadata
// download. These mirror the Next dashboard's /api/auth/saml/test and
// /api/auth/saml/metadata routes.
package sso

import (
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

const (
	samlACSPath     = "/api/auth/saml/acs"
	samlMetadataPth = "/api/auth/saml/metadata"
)

// Handler serves the dashboard SSO settings routes.
type Handler struct {
	Repo *db.Repo
}

// NewHandler creates an SSO settings handler bound to the repo.
func NewHandler(repo *db.Repo) *Handler {
	return &Handler{Repo: repo}
}

// HandleSamlTest handles POST /api/auth/saml/test: validate the SSO URL, SP
// issuer and IdP certificate before the settings are saved.
func (h *Handler) HandleSamlTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SamlEntryPoint string `json:"samlEntryPoint"`
		SamlIssuer     string `json:"samlIssuer"`
		SamlCert       string `json:"samlCert"`
	}
	decodeJSONBody(r, &body)

	settings, _ := h.Repo.GetSettingsRaw()
	if settings == nil {
		settings = map[string]any{}
	}

	entryPoint := strings.TrimSpace(firstNonEmpty(body.SamlEntryPoint, handlerutil.GetString(settings, "samlEntryPoint")))
	issuer := strings.TrimSpace(firstNonEmpty(body.SamlIssuer, handlerutil.GetString(settings, "samlIssuer"), "urn:9router:sp"))
	cert := strings.TrimSpace(firstNonEmpty(body.SamlCert, handlerutil.GetString(settings, "samlCert")))

	if entryPoint == "" {
		writeError(w, http.StatusBadRequest, "Single Sign-On Service URL (samlEntryPoint) is required")
		return
	}
	if _, err := url.ParseRequestURI(entryPoint); err != nil {
		writeError(w, http.StatusBadRequest, "Single Sign-On Service URL must be a valid URL")
		return
	}
	if issuer == "" {
		writeError(w, http.StatusBadRequest, "SP Entity ID / Issuer (samlIssuer) is required")
		return
	}
	if cert == "" {
		writeError(w, http.StatusBadRequest, "IdP X.509 Certificate (samlCert) is required")
		return
	}
	if !validCertificate(cert) {
		writeError(w, http.StatusBadRequest, "Invalid IdP X.509 Certificate format")
		return
	}

	origin := publicOrigin(r)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"samlEntryPoint": entryPoint,
		"samlIssuer":     issuer,
		"certValid":      true,
		"acsUrl":         origin + samlACSPath,
		"metadataUrl":    origin + samlMetadataPth,
		"message":        "SAML 2.0 configuration verified successfully.",
	})
}

// HandleSamlMetadata handles GET /api/auth/saml/metadata: serve the SAML
// Service Provider metadata XML for the configured issuer and ACS URL.
func (h *Handler) HandleSamlMetadata(w http.ResponseWriter, r *http.Request) {
	settings, err := h.Repo.GetSettingsRaw()
	if err != nil {
		writeMetadataError(w, err.Error())
		return
	}
	issuer := handlerutil.GetString(settings, "samlIssuer")
	if issuer == "" {
		issuer = "urn:9router:sp"
	}
	origin := publicOrigin(r)
	acsURL := origin + samlACSPath

	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, spMetadataXML(issuer, acsURL))
}

// spMetadataXML renders the Service Provider metadata document IdPs expect.
func spMetadataXML(issuer, acsURL string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>`+"\n"+
		`<md:EntityDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata" entityID="%s">`+"\n"+
		`  <md:SPSSODescriptor AuthnRequestsSigned="false" WantAssertionsSigned="false" protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">`+"\n"+
		`    <md:NameIDFormat>urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress</md:NameIDFormat>`+"\n"+
		`    <md:AssertionConsumerService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST" Location="%s" index="0"/>`+"\n"+
		`  </md:SPSSODescriptor>`+"\n"+
		`</md:EntityDescriptor>`+"\n",
		xmlEscape(issuer), xmlEscape(acsURL))
}

// validCertificate accepts a PEM block or raw base64 DER and reports whether it
// parses as an X.509 certificate.
func validCertificate(cert string) bool {
	der := formatCertificate(cert)
	if len(der) == 0 {
		return false
	}
	_, err := x509.ParseCertificate(der)
	return err == nil
}

// formatCertificate normalizes a certificate to DER bytes, mirroring Next's
// formatX509Certificate (strip headers, keep base64 body, then decode).
func formatCertificate(cert string) []byte {
	if strings.TrimSpace(cert) == "" {
		return nil
	}
	cleaned := strings.ReplaceAll(cert, "-----BEGIN CERTIFICATE-----", "")
	cleaned = strings.ReplaceAll(cleaned, "-----END CERTIFICATE-----", "")

	var b strings.Builder
	for _, r := range cleaned {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '+' || r == '/' || r == '=' {
			b.WriteRune(r)
		}
	}
	encoded := b.String()
	if encoded == "" {
		return nil
	}
	der, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// Tolerate PEM-style line breaks that survived the filter.
		der, err = base64.StdEncoding.DecodeString(strings.ReplaceAll(encoded, "\n", ""))
		if err != nil {
			return nil
		}
	}
	return der
}

// decodeJSONBody parses the request body, tolerating a missing or invalid body
// the way Next's body.json().catch(() => ({})) does.
func decodeJSONBody(r *http.Request, dst any) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return
	}
	_ = json.Unmarshal(body, dst)
}

// publicOrigin resolves the externally visible origin, honoring BASE_URL and
// forwarded headers the same way the Next dashboard does.
func publicOrigin(r *http.Request) string {
	if base := strings.TrimSpace(os.Getenv("BASE_URL")); base != "" {
		return strings.TrimRight(base, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		if p := strings.ToLower(strings.Split(proto, ",")[0]); p == "https" || p == "http" {
			scheme = p
		}
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

// firstNonEmpty returns the first non-blank value.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// writeError answers with Next's flat { error: "message" } shape.
func writeError(w http.ResponseWriter, status int, message string) {
	handlerutil.WriteJSON(w, status, map[string]any{"error": message})
}

// writeMetadataError answers with Next's XML error document.
func writeMetadataError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprintf(w, `<?xml version="1.0"?><Error>%s</Error>`, xmlEscape(message))
}

// xmlEscape escapes XML text nodes/attributes.
func xmlEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
