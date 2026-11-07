package oauth

import (
	"sync"
	"time"
)

// antigravityRedirectURI is the redirect target of agy's own sign-in flow:
// a Google-hosted page (antigravity.google/oauth-callback) that displays
// the authorization code for the user to copy back into the application.
// It is registered for the public Antigravity client, so the authorize step
// pins it verbatim and the exchange repeats it — the dashboard-derived
// /callback the other providers use is never sent for Antigravity
// (teardown §7; fragment 982).
const antigravityRedirectURI = "https://antigravity.google/oauth-callback"

// antigravityVerifierTTL bounds how long a minted PKCE verifier waits for
// its exchange. agy's flow is a quick copy-paste round trip; ten minutes is
// generous without leaving verifiers valid indefinitely.
const antigravityVerifierTTL = 10 * time.Minute

// antigravityVerifiers is the process-wide store of in-flight Antigravity
// logins, keyed by the OAuth state the authorize step minted. It follows
// the Codex session registry's shape (mutex-guarded map, TTL sweep on
// write, drop-on-read) because the generic PKCE handlers cannot be reused
// here: they hand verifier custody to the client (their authorize JSON
// returns codeVerifier), while agy's flow keeps the verifier server-side
// and consumes it at exchange time — and this provider's exchange is
// bespoke anyway (projectId preservation, the fragment-972 identity pin),
// so it cannot ride completePKCEExchange/persistConnection.
var antigravityVerifiers = &antigravityVerifierStore{byState: map[string]antigravityVerifier{}}

type antigravityVerifier struct {
	verifier  string
	createdAt time.Time
}

type antigravityVerifierStore struct {
	mu      sync.Mutex
	byState map[string]antigravityVerifier
}

// put records the verifier minted for state, sweeping expired entries.
func (s *antigravityVerifierStore) put(state, verifier string) {
	if state == "" || verifier == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-antigravityVerifierTTL)
	for k, v := range s.byState {
		if v.createdAt.Before(cutoff) {
			delete(s.byState, k)
		}
	}
	s.byState[state] = antigravityVerifier{verifier: verifier, createdAt: time.Now()}
}

// consume removes and returns the verifier for state. Removal is
// unconditional — a verifier is single-use whether the exchange that
// follows succeeds or not — and an expired entry reports as missing.
func (s *antigravityVerifierStore) consume(state string) (string, bool) {
	if state == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byState[state]
	if !ok {
		return "", false
	}
	delete(s.byState, state)
	if time.Since(entry.createdAt) > antigravityVerifierTTL {
		return "", false
	}
	return entry.verifier, true
}
