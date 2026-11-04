package oauth

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"html"
	"net"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// Loopback callback proxies + session registries behind the generic
// {provider}/{action} route (AxonRouter src/lib/oauth/utils/server.js).
// Codex already has its fixed-port proxy in codex_proxy.go; this file adds
// the parallel xAI fixed-port proxy and the dynamic-port proxies for
// trae / windsurf / zed / xiaomi-mimo, each with the session registry its
// poll-status action reads.

var loopbackOriginRe = regexp.MustCompile(`^http://(127\.0\.0\.1|localhost)(:\d+)?$`)

// isLoopbackOrigin mirrors Axon's guard: legit OAuth redirects are top-level
// navigations (no Origin header); a cross-site fetch always sends one.
func isLoopbackOrigin(origin string) bool {
	if origin == "" {
		return true
	}
	return loopbackOriginRe.MatchString(origin)
}

// oauthResultPage renders Axon's shared callback result page.
func oauthResultPage(success bool, message string) string {
	color, icon, title := "#ef4444", "&#10007;", "Authentication Failed"
	if success {
		color, icon, title = "#22c55e", "&#10003;", "Authentication Successful"
	}
	if message == "" {
		if success {
			message = "You can close this window."
		} else {
			message = "Please try again."
		}
	}
	return `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>` + title + `</title>
<style>body{font-family:system-ui;display:flex;justify-content:center;align-items:center;height:100vh;margin:0;background:#f5f5f5}.c{text-align:center;padding:2rem;background:#fff;border-radius:8px;box-shadow:0 2px 10px rgba(0,0,0,.1)}.i{color:` + color + `;font-size:3rem}h1{margin:1rem 0}p{color:#666}</style>
</head><body><div class="c"><div class="i">` + icon + `</div><h1>` + title + `</h1><p>` + html.EscapeString(message) + `</p><p>Closing in <span id="cd">3</span>s...</p>
<script>let n=3;const c=document.getElementById("cd");const t=setInterval(()=>{n--;c.textContent=n;if(n<=0){clearInterval(t);window.close();}},1000);</script>
</div></body></html>`
}

// mimoResultPage renders the Xiaomi MiMo callback page (no countdown).
func mimoResultPage(success bool, message string) string {
	color, icon, title := "#ef4444", "&#10007;", "Authentication Failed"
	if success {
		color, icon, title = "#22c55e", "&#10003;", "Authentication Successful"
	}
	if message == "" {
		if success {
			message = "You can close this tab and return to AxonRouter."
		} else {
			message = "Please try again."
		}
	}
	autoClose := ""
	if success {
		autoClose = "<script>setTimeout(() => window.close(), 3000);</script>"
	}
	return `<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>` + title + `</title>
<style>
  body { font-family: system-ui; display: flex; justify-content: center; align-items: center; height: 100vh; margin: 0; background: #f5f5f5; }
  .container { text-align: center; padding: 2rem; background: white; border-radius: 8px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); }
  .icon { color: ` + color + `; font-size: 3rem; }
  h1 { margin: 1rem 0; font-size: 1.25rem; }
  p { color: #666; font-size: 0.875rem; }
</style>
</head>
<body>
  <div class="container">
    <div class="icon">` + icon + `</div>
    <h1>` + title + `</h1>
    <p>` + html.EscapeString(message) + `</p>
    ` + autoClose + `
  </div>
</body>
</html>`
}

func writeHTMLPage(w http.ResponseWriter, status int, page string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(page))
}

// ---------- dynamic-port proxies: trae / windsurf / zed ----------

// dynOAuthSession is one singleton login session for a dynamic-port proxy.
type dynOAuthSession struct {
	state        string
	codeVerifier string
	systemID     string
	status       string // pending | done | error
	createdAt    time.Time
	connectionID string
	email        string
	errMsg       string
}

// dynOAuthProxy is a dynamic-port loopback listener with a singleton
// session, the shape Axon uses for trae, windsurf and zed.
type dynOAuthProxy struct {
	mu       sync.Mutex
	provider string
	listener net.Listener
	srv      *http.Server
	port     int
	idle     *time.Timer
	session  *dynOAuthSession
	handler  *OAuthHandler
}

var (
	traeDynProxy     = &dynOAuthProxy{provider: "trae"}
	windsurfDynProxy = &dynOAuthProxy{provider: "windsurf"}
	zedDynProxy      = &dynOAuthProxy{provider: "zed"}
)

func dynProxyFor(provider string) *dynOAuthProxy {
	switch provider {
	case "trae":
		return traeDynProxy
	case "windsurf":
		return windsurfDynProxy
	case "zed":
		return zedDynProxy
	}
	return nil
}

// callbackPath is where the provider's IDE/browser lands on this proxy.
func (p *dynOAuthProxy) callbackPath() string {
	switch p.provider {
	case "trae":
		return "/callback"
	case "windsurf":
		return "/windsurf-auth-callback"
	default: // zed
		return "/"
	}
}

func (p *dynOAuthProxy) displayName() string {
	switch p.provider {
	case "trae":
		return "Trae"
	case "windsurf":
		return "Windsurf"
	default:
		return "Zed"
	}
}

// register stores the pending login session (Axon register*Session).
func (p *dynOAuthProxy) register(state, codeVerifier, systemID string) bool {
	if state == "" {
		return false
	}
	if p.provider == "zed" && codeVerifier == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.session = &dynOAuthSession{
		state: state, codeVerifier: codeVerifier, systemID: systemID,
		status: "pending", createdAt: time.Now(),
	}
	return true
}

// get returns a copy of the session when the state matches (Axon
// get*SessionStatus: a mismatched state reads as no session).
func (p *dynOAuthProxy) get(state string) (dynOAuthSession, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.session == nil {
		return dynOAuthSession{}, false
	}
	if state != "" && p.session.state != state {
		return dynOAuthSession{}, false
	}
	return *p.session, true
}

func (p *dynOAuthProxy) clear(state string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if state == "" || (p.session != nil && p.session.state == state) {
		p.session = nil
	}
}

// start binds the listener (preferred port first; zed falls back to a random
// port when its preferred one is busy, like Axon). Returns Axon's
// {success, port, callbackUrl} triple or (false, "", reason).
func (p *dynOAuthProxy) start(h *OAuthHandler, preferredPort int) (bool, int, string, string) {
	p.mu.Lock()
	if p.listener != nil {
		port := p.port
		p.handler = h
		if p.provider == "zed" {
			// Zed renews the idle deadline on reuse so a previous flow's
			// deadline cannot kill the flow that just adopted the port.
			if p.idle != nil {
				p.idle.Stop()
			}
			p.idle = time.AfterFunc(10*time.Minute, p.stop)
		}
		p.mu.Unlock()
		return true, port, fmt.Sprintf("http://127.0.0.1:%d%s", port, p.callbackPath()), ""
	}
	p.mu.Unlock()

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(preferredPort)))
	if err != nil && p.provider == "zed" && preferredPort != 0 && isAddrInUse(err) {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return false, 0, "", err.Error()
	}
	port := ln.Addr().(*net.TCPAddr).Port
	p.mu.Lock()
	p.listener = ln
	p.port = port
	p.handler = h
	srv := &http.Server{Handler: http.HandlerFunc(p.serveHTTP), ReadHeaderTimeout: 10 * time.Second}
	p.srv = srv
	if p.idle != nil {
		p.idle.Stop()
	}
	p.idle = time.AfterFunc(10*time.Minute, p.stop)
	p.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	return true, port, fmt.Sprintf("http://127.0.0.1:%d%s", port, p.callbackPath()), ""
}

func (p *dynOAuthProxy) stop() {
	p.mu.Lock()
	srv, idle, ln := p.srv, p.idle, p.listener
	p.srv, p.listener, p.idle = nil, nil, nil
	p.port = 0
	p.mu.Unlock()
	if idle != nil {
		idle.Stop()
	}
	if ln != nil {
		_ = ln.Close()
	}
	if srv != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(ctx)
		}()
	}
}

func (p *dynOAuthProxy) serveHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch p.provider {
	case "trae":
		if path != "/callback" && path != "/auth/callback" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("Not found"))
			return
		}
	case "windsurf":
		if path != "/windsurf-auth-callback" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("Not found"))
			return
		}
	case "zed":
		if path != "/" && path != "/callback" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("Not found"))
			return
		}
	}
	p.mu.Lock()
	sess := p.session
	h := p.handler
	p.mu.Unlock()
	if sess == nil {
		writeHTMLPage(w, http.StatusOK, oauthResultPage(false, "No active "+p.displayName()+" login session"))
		return
	}
	if !isLoopbackOrigin(r.Header.Get("Origin")) {
		writeHTMLPage(w, http.StatusForbidden, oauthResultPage(false, "Cross-origin callback rejected"))
		return
	}
	q := r.URL.Query()
	if p.provider == "trae" {
		if cbState := q.Get("state"); cbState != "" && sess.state != "" && cbState != sess.state {
			p.finishWithError(sess, "Trae callback state mismatch", w, true)
			return
		}
	}
	if p.provider == "windsurf" {
		cbState := q.Get("state")
		if cbState == "" || sess.state == "" || cbState != sess.state {
			p.finishWithError(sess, "Windsurf callback state mismatch", w, true)
			return
		}
	}
	if p.provider == "zed" {
		hasParams := false
		for _, k := range []string{"user_id", "userId", "access_token", "accessToken", "token"} {
			if _, ok := q[k]; ok {
				hasParams = true
				break
			}
		}
		if !hasParams {
			// A probe/prefetch, not the callback: keep session + listener.
			writeHTMLPage(w, http.StatusOK, oauthResultPage(false, "Waiting for Zed sign-in — this request carried no login data."))
			return
		}
	}
	rawCallback := path
	if r.URL.RawQuery != "" {
		rawCallback = path + "?" + r.URL.RawQuery
	}
	status, body := p.runExchange(h, sess, rawCallback)
	if status == http.StatusOK && (body["status"] == "authorized" || body["success"] == true) {
		connID, email := capturedConnection(body)
		p.mu.Lock()
		if p.session != nil {
			p.session.status = "done"
			p.session.connectionID = connID
			p.session.email = email
		}
		p.mu.Unlock()
		writeHTMLPage(w, http.StatusOK, oauthResultPage(true, "You can close this window."))
		p.stop()
		return
	}
	msg := capturedErrorMessage(body)
	if msg == "" {
		msg = "exchange failed"
	}
	// Zed keeps the listener on failure: the failed attempt may belong to a
	// superseded popup, and the live attempt's callback must still land.
	p.finishWithError(sess, msg, w, p.provider != "zed")
}

// finishWithError marks the session errored, renders the failure page, and
// optionally stops the listener (trae/windsurf semantics).
func (p *dynOAuthProxy) finishWithError(sess *dynOAuthSession, msg string, w http.ResponseWriter, stop bool) {
	p.mu.Lock()
	if p.session != nil && sess != nil && p.session.state == sess.state {
		p.session.status = "error"
		p.session.errMsg = msg
	}
	p.mu.Unlock()
	writeHTMLPage(w, http.StatusOK, oauthResultPage(false, msg))
	if stop {
		p.stop()
	}
}

// runExchange completes the callback through the provider's dedicated
// exchange handler, captured so its outcome can be folded into the session.
func (p *dynOAuthProxy) runExchange(h *OAuthHandler, sess *dynOAuthSession, rawCallback string) (int, map[string]any) {
	if h == nil {
		return http.StatusInternalServerError, nil
	}
	var payload map[string]any
	var fn func(http.ResponseWriter, *http.Request)
	switch p.provider {
	case "trae":
		payload = map[string]any{"code": rawCallback}
		fn = h.HandleTraeExchange
	case "windsurf":
		payload = map[string]any{"code": rawCallback, "state": sess.state}
		fn = h.HandleWindsurfExchange
	case "zed":
		payload = map[string]any{"code": rawCallback, "codeVerifier": sess.codeVerifier, "systemId": sess.systemID}
		fn = h.HandleZedExchange
	default:
		return http.StatusInternalServerError, nil
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1/api/oauth/"+p.provider+"/exchange", bytes.NewReader(raw))
	if err != nil {
		return http.StatusInternalServerError, nil
	}
	req.Header.Set("Content-Type", "application/json")
	return runCaptured(func(w http.ResponseWriter) { fn(w, req) })
}

// capturedConnection pulls (id, email) out of a captured exchange success
// body in either of this package's two success shapes.
func capturedConnection(body map[string]any) (string, string) {
	if body == nil {
		return "", ""
	}
	if conn, ok := body["connection"].(map[string]any); ok {
		id, _ := conn["id"].(string)
		email, _ := conn["email"].(string)
		return id, email
	}
	id, _ := body["connectionId"].(string)
	if id == "" {
		id, _ = body["id"].(string)
	}
	email, _ := body["email"].(string)
	return id, email
}
