package handlerutil

import (
	"net/http"
	"strings"
)

// RequestOrigin reconstructs the origin the request arrived on — scheme and
// host — for handlers that call back into this same server (combo health
// checks, request replay). AxonRouter's replay builds its dispatch URL from
// the request origin the same way; the forwarded proto wins when a tunnel or
// proxy fronts the gateway, then TLS, then plain HTTP.
func RequestOrigin(r *http.Request) string {
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = strings.TrimSpace(strings.SplitN(proto, ",", 2)[0])
	} else if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
