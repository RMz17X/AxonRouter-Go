package oauth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// The Xiaomi MiMo dynamic-port callback proxy (split from
// batch3_proxy.go): an ECDH callback listener plus the session registry
// its poll-status and exchange actions read.

// ---------- Xiaomi MiMo dynamic-port proxy ----------

// mimoDynSession is one pending ECDH login; the verifier string carries the
// X25519 private key in this package's "mimo-x25519:" encoding.
type mimoDynSession struct {
	verifier  string
	status    string // pending | done | error
	createdAt time.Time
	uid       string
	sk        string
	baseURL   string
	errMsg    string
}

// mimoDynProxyState is the MiMo callback listener + session map.
type mimoDynProxyState struct {
	mu       sync.Mutex
	listener net.Listener
	srv      *http.Server
	port     int
	idle     *time.Timer
	sessions map[string]*mimoDynSession
}

var mimoDynProxy = &mimoDynProxyState{sessions: map[string]*mimoDynSession{}}

func (p *mimoDynProxyState) register(state, verifier string) bool {
	if state == "" || verifier == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessions[state] = &mimoDynSession{verifier: verifier, status: "pending", createdAt: time.Now()}
	return true
}

// lookup returns a copy of the session for poll-status / exchange. The
// private key never leaves the registry (Axon's getXiaomiMimoSessionStatus
// exposes only {status, result, error}).
func (p *mimoDynProxyState) lookup(state string) (mimoDynSession, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	sess, ok := p.sessions[state]
	if !ok {
		return mimoDynSession{}, false
	}
	return *sess, true
}

func (p *mimoDynProxyState) clear(state string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sessions, state)
}

func (p *mimoDynProxyState) start() (bool, int, string, string) {
	p.mu.Lock()
	if p.listener != nil {
		port := p.port
		p.mu.Unlock()
		return true, port, fmt.Sprintf("http://127.0.0.1:%d/", port), ""
	}
	p.mu.Unlock()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return false, 0, "", err.Error()
	}
	port := ln.Addr().(*net.TCPAddr).Port
	p.mu.Lock()
	p.listener = ln
	p.port = port
	srv := &http.Server{Handler: http.HandlerFunc(p.serveHTTP), ReadHeaderTimeout: 10 * time.Second}
	p.srv = srv
	if p.idle != nil {
		p.idle.Stop()
	}
	p.idle = time.AfterFunc(5*time.Minute, p.stop)
	p.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	return true, port, fmt.Sprintf("http://127.0.0.1:%d/", port), ""
}

func (p *mimoDynProxyState) stop() {
	p.mu.Lock()
	srv, idle, ln := p.srv, p.idle, p.listener
	p.srv, p.listener, p.idle = nil, nil, nil
	p.port = 0
	// No callback can arrive once the listener is down, so drop every
	// pending session — each holds an X25519 private key.
	p.sessions = map[string]*mimoDynSession{}
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

func (p *mimoDynProxyState) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackOrigin(r.Header.Get("Origin")) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Forbidden"))
		return
	}
	u := r.URL.Query().Get("u")
	if u == "" {
		writeHTMLPage(w, http.StatusBadRequest, mimoResultPage(false, "Missing encrypted payload (u parameter)."))
		return
	}
	p.mu.Lock()
	pending := make([]*mimoDynSession, 0, len(p.sessions))
	for _, s := range p.sessions {
		if s.status == "pending" {
			pending = append(pending, s)
		}
	}
	p.mu.Unlock()
	if len(pending) == 0 {
		writeHTMLPage(w, http.StatusInternalServerError, mimoResultPage(false, "No active OAuth session. Please restart the login flow."))
		return
	}
	// The callback carries no state — try each pending session's key.
	var matched *mimoDynSession
	var payload mimoPayload
	var lastErr error
	for _, sess := range pending {
		dec, err := mimoDecryptCallback(sess.verifier, u)
		if err == nil {
			matched, payload = sess, dec
			break
		}
		lastErr = err
	}
	if matched == nil || payload.sk == "" {
		msg := "Could not decrypt with any pending session key"
		if matched != nil && payload.sk == "" {
			msg = "Decrypted payload missing sk (API key)"
		} else if lastErr != nil {
			msg = lastErr.Error()
		}
		p.mu.Lock()
		for _, sess := range pending {
			sess.status = "error"
			sess.errMsg = msg
		}
		p.mu.Unlock()
		writeHTMLPage(w, http.StatusBadRequest, mimoResultPage(false, "Decryption failed: "+msg))
		return
	}
	p.mu.Lock()
	matched.status = "done"
	matched.uid = payload.uid
	matched.sk = payload.sk
	matched.baseURL = payload.url
	p.mu.Unlock()
	writeHTMLPage(w, http.StatusOK, mimoResultPage(true, "Xiaomi account linked. You can close this tab."))
}
