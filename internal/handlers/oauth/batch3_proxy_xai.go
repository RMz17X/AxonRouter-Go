package oauth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// The xAI fixed-port loopback proxy (split from batch3_proxy.go).

// ---------- xAI fixed-port proxy (127.0.0.1:56121) ----------

const xaiProxyPort = 56121

// xaiSession is one registered server-side xAI login (Axon xaiPendingExchanges).
type xaiSession struct {
	codeVerifier string
	redirectURI  string
	status       string // pending | done | error
	createdAt    time.Time
	connectionID string
	email        string
	errMsg       string
}

// xaiProxyState is the xAI loopback listener + session registry. Axon keeps
// it a parallel implementation of the Codex one rather than generalizing;
// this mirrors that choice (codex_proxy.go stays untouched).
type xaiProxyState struct {
	mu       sync.Mutex
	listener net.Listener
	srv      *http.Server
	appPort  string
	idle     *time.Timer
	sessions map[string]*xaiSession
	handler  *OAuthHandler
}

var xaiLoopback = &xaiProxyState{sessions: map[string]*xaiSession{}}

func (p *xaiProxyState) register(state, codeVerifier, redirectURI string) bool {
	if state == "" || codeVerifier == "" || redirectURI == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessions[state] = &xaiSession{
		codeVerifier: codeVerifier, redirectURI: redirectURI,
		status: "pending", createdAt: time.Now(),
	}
	return true
}

func (p *xaiProxyState) lookup(state string) (xaiSession, bool) {
	if state == "" {
		return xaiSession{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	sess, ok := p.sessions[state]
	if !ok {
		return xaiSession{}, false
	}
	return *sess, true
}

func (p *xaiProxyState) clear(state string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sessions, state)
}

// start binds the fixed xAI port. Returns (success, reason).
func (p *xaiProxyState) start(h *OAuthHandler, appPort string) (bool, string) {
	p.mu.Lock()
	if p.listener != nil {
		p.appPort = appPort
		p.handler = h
		p.mu.Unlock()
		return true, ""
	}
	p.mu.Unlock()
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(xaiProxyPort)))
	if err != nil {
		if isAddrInUse(err) {
			return false, "port_busy"
		}
		return false, err.Error()
	}
	p.mu.Lock()
	p.listener = ln
	p.appPort = appPort
	p.handler = h
	srv := &http.Server{Handler: http.HandlerFunc(p.serveHTTP), ReadHeaderTimeout: 10 * time.Second}
	p.srv = srv
	if p.idle != nil {
		p.idle.Stop()
	}
	p.idle = time.AfterFunc(5*time.Minute, p.stop)
	p.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	return true, ""
}

func (p *xaiProxyState) stop() {
	p.mu.Lock()
	srv, idle, ln := p.srv, p.idle, p.listener
	p.srv, p.listener, p.idle = nil, nil, nil
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

func (p *xaiProxyState) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/callback" && r.URL.Path != "/auth/callback" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Not found"))
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	sess, registered := p.lookup(state)
	if !registered {
		// Mode B: legacy channel fallback — bounce to the app callback.
		p.mu.Lock()
		appPort := p.appPort
		p.mu.Unlock()
		target := "http://localhost:" + appPort + "/callback"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		w.Header().Set("Location", target)
		w.WriteHeader(http.StatusFound)
		p.stop()
		return
	}
	// Mode A: server-side exchange through the shared PKCE machinery.
	fail := func(err error) {
		p.mu.Lock()
		if s, ok := p.sessions[state]; ok {
			s.status = "error"
			s.errMsg = err.Error()
		}
		p.mu.Unlock()
		writeHTMLPage(w, http.StatusOK, oauthResultPage(false, err.Error()))
		p.stop()
	}
	if errParam := q.Get("error"); errParam != "" {
		fail(fmt.Errorf("%s", firstNonEmpty(q.Get("error_description"), errParam)))
		return
	}
	code := q.Get("code")
	if code == "" {
		fail(fmt.Errorf("No authorization code received"))
		return
	}
	p.mu.Lock()
	h := p.handler
	p.mu.Unlock()
	ex := &pkceExchange{
		cfg:          pkceProviders["xai"],
		provider:     "xai",
		code:         code,
		codeVerifier: sess.codeVerifier,
		state:        state,
		redirectURI:  sess.redirectURI,
		clientID:     pkceProviders["xai"].clientID,
		tokenURL:     pkceProviders["xai"].tokenURL,
	}
	if disc, err := discoverOIDCEndpoints(pkceProviders["xai"].discoverIssuer); err == nil {
		ex.tokenURL = disc.tokenURL
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	res, xerr := h.completePKCEExchange(ctx, ex)
	if xerr != nil {
		fail(fmt.Errorf("%s", xerr.message))
		return
	}
	p.mu.Lock()
	if s, ok := p.sessions[state]; ok {
		s.status = "done"
		s.connectionID = res.ConnectionID
		s.email = res.Email
	}
	p.mu.Unlock()
	writeHTMLPage(w, http.StatusOK, oauthResultPage(true, "You can close this window."))
	p.stop()
}
