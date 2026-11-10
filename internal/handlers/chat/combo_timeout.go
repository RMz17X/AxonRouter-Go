package chat

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"axonrouter/proxy/internal/log"
)

// comboResponseTimeout serializes timeout expiry with response commitment. A
// canceled candidate can never write into the next candidate's response. Once
// committed, the timer is disarmed without canceling the live upstream stream.
//
// The timer is a STALL timer, not an absolute deadline: it bounds the
// upstream silence a candidate may fall into, and every upstream progress
// signal (response headers, any body chunk read — reported through
// noteUpstreamProgress) restarts the full window. Only a candidate that
// stays completely quiet for the whole span is canceled; a slow-starting
// candidate that keeps progressing is never cut.
type comboResponseTimeout struct {
	http.ResponseWriter
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	timer     *time.Timer
	timeout   time.Duration
	committed bool
	expired   bool
	header    http.Header
}

func newComboResponseTimeout(ctx context.Context, w http.ResponseWriter, timeout time.Duration) *comboResponseTimeout {
	ctx, cancel := context.WithCancel(ctx)
	t := &comboResponseTimeout{ResponseWriter: w, cancel: cancel, header: make(http.Header), timeout: timeout}
	// The gate rides its own context, so the serving paths running under it
	// can report upstream progress without knowing the gate exists. The
	// registry lives in internal/log (next to the request-ID and account
	// keys) because the executor package reports progress too and cannot
	// import this package.
	t.ctx = log.RegisterProgressGate(ctx, t)
	t.timer = time.AfterFunc(timeout, t.expire)
	return t
}

// noteUpstreamProgress tells the combo timeout gate registered in ctx (if
// any) that the upstream just showed a sign of life, restarting its stall
// window. Contexts without a gate — direct requests, the last combo
// candidate — make it a no-op. It delegates to the shared registry in
// internal/log, the same one the response-body wrapper and the executor
// package report through: one mechanism, several call sites.
func noteUpstreamProgress(ctx context.Context) {
	log.NoteUpstreamProgress(ctx)
}

// Progress records upstream activity: while the candidate is uncommitted
// and the gate has not fired, the stall window restarts in full. After
// commit the timer is already stopped, and after expiry a late signal must
// not resurrect the gate — the candidate stays canceled.
func (t *comboResponseTimeout) Progress() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.committed || t.expired {
		return
	}
	t.timer.Reset(t.timeout)
}

func (t *comboResponseTimeout) expire() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.committed {
		t.expired = true
		t.cancel()
	}
}

func (t *comboResponseTimeout) Header() http.Header {
	return t.header
}

func (t *comboResponseTimeout) commitLocked() bool {
	if t.expired {
		return false
	}
	if !t.committed {
		for key, values := range t.header {
			t.ResponseWriter.Header()[key] = values
		}
	}
	t.committed = true
	t.timer.Stop()
	return true
}

func (t *comboResponseTimeout) WriteHeader(code int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.commitLocked() {
		t.ResponseWriter.WriteHeader(code)
	}
}

func (t *comboResponseTimeout) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.commitLocked() {
		return 0, context.DeadlineExceeded
	}
	return t.ResponseWriter.Write(b)
}

func (t *comboResponseTimeout) Flush() {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Flush alone is not usable output and must not disarm the timer.
	if t.committed {
		if f, ok := t.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func (t *comboResponseTimeout) finish() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.timer.Stop()
	t.cancel()
	return t.expired
}

func (t *comboResponseTimeout) timedOut() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.expired
}

// Volatile free-tier providers (owner directive, 2026-11-10): the
// client must stay fast, and the free tiers that are slow or
// error-prone — dahl, apinex — get a special system instead of the
// generic combo treatment:
//
//   - a short upstream-silence window per candidate (their failure
//     mode is the network-wide stall: connection accepted, no token
//     ever arrives; dahl burned the full 300 s combo window per stall,
//     measured 341-354 s, before the chain moved on), and
//   - a provider-wide circuit breaker: one member's stall OR a member
//     that burns its attempts on upstream errors benches every entry
//     of that provider in the combo for a few minutes, so subsequent
//     requests skip the sick provider entirely instead of paying its
//     tax one by one. Re-admission is automatic and cheap: the first
//     request after the bench pays at most the short silence window.
//
// Thirty seconds of total silence is already generous for these
// providers; progress signals restart the window as usual, so a
// slow-but-alive stream is never cut.
type volatilePolicy struct {
	stallTimeout  time.Duration
	benchDuration time.Duration
}

// volatilePolicyFor returns the special-system policy for a provider,
// or false when the provider gets the generic treatment. Provider ids
// are the resolved ids the serving path carries (apinex resolves to
// its openai-compatible id), matching how hedging keys its policies.
func volatilePolicyFor(provider string) (volatilePolicy, bool) {
	switch provider {
	case "dahl", "openai-compatible-chat-apinex":
		return volatilePolicy{stallTimeout: 30 * time.Second, benchDuration: 3 * time.Minute}, true
	}
	return volatilePolicy{}, false
}

// candidateStallTimeout returns the silence window for one combo
// candidate: the combo's configured window, shortened for volatile
// free-tier providers.
func candidateStallTimeout(provider string, comboTimeout time.Duration) time.Duration {
	if pol, ok := volatilePolicyFor(provider); ok && comboTimeout > pol.stallTimeout {
		return pol.stallTimeout
	}
	return comboTimeout
}

// benchProviderModels benches every entry of one provider in a combo
// for the grace period. benchTimedOutModel keys on single entries,
// which is right for a slow model, but a provider-wide failure (a dahl
// network stall) would otherwise leave the provider's other entries in
// the chain to stall one after another.
func (h *ChatHandler) benchProviderModels(comboName string, models []string, provider string, grace time.Duration) {
	for _, m := range models {
		if info := h.resolveModelEntry(m); info != nil && info.Provider == provider {
			h.benchTimedOutModel(comboName, m, grace)
		}
	}
}

// noteGateTimeout benches a candidate whose stall gate fired:
// provider-wide for volatile providers, whose stalls are a network
// property, and per-entry otherwise. Callers pair it with setting
// their lastErr to comboTimeoutError() and modelTimedOut to true.
func (h *ChatHandler) noteGateTimeout(comboName string, models []string, entry string, provider string, grace time.Duration) {
	if pol, ok := volatilePolicyFor(provider); ok {
		h.benchProviderModels(comboName, models, provider, pol.benchDuration)
		return
	}
	h.benchTimedOutModel(comboName, entry, grace)
}

// noteVolatileFailure benches a volatile provider whose member just
// failed on upstream errors (as opposed to a stall, which
// noteGateTimeout covers): the member sent at least one attempt, none
// succeeded, and the final error is an upstream-class failure (5xx,
// 429, or a wrapped transport error, which the chain records as 502),
// never a client-class 4xx — a bad request must not park a provider.
// The bench is provider-wide, like the stall bench: apinex answering
// every account with the same instant 499 is a provider outage, and
// the next requests should skip it until it recovers.
func (h *ChatHandler) noteVolatileFailure(comboName string, models []string, provider string, attempts int, lastErr error) {
	pol, ok := volatilePolicyFor(provider)
	if !ok || attempts <= 0 || lastErr == nil {
		return
	}
	var ue *upstreamError
	if !errors.As(lastErr, &ue) || ue == nil {
		return
	}
	if ue.StatusCode < 500 && ue.StatusCode != http.StatusTooManyRequests {
		return
	}
	h.benchProviderModels(comboName, models, provider, pol.benchDuration)
}

// comboTimeoutPolicy returns the per-candidate upstream-silence window and
// the grace period a timed-out model sits out. Both are zero when the
// timeout is off.
func (h *ChatHandler) comboTimeoutPolicy(comboName string) (timeout, grace time.Duration) {
	settings, err := h.Repo.GetSettings()
	if err != nil || settings == nil {
		return 0, 0
	}
	cfg := settings.ComboStrategies[comboName]
	if cfg.FirstTokenTimeoutMs <= 0 {
		return 0, 0
	}
	return time.Duration(cfg.FirstTokenTimeoutMs) * time.Millisecond,
		time.Duration(cfg.FirstTokenGrace()) * time.Millisecond
}

func comboBenchKey(comboName, entry string) string {
	return comboName + "\x00" + entry
}

// benchTimedOutModel keeps a model that missed its response timeout out of the
// combo chain until the grace period has elapsed. The state is in memory on
// purpose: a restart is a fresh start, and a stale bench must never outlive
// the process that observed the slowness.
func (h *ChatHandler) benchTimedOutModel(comboName, entry string, grace time.Duration) {
	if grace <= 0 {
		return
	}
	now := time.Now()
	h.timeoutBenchMu.Lock()
	defer h.timeoutBenchMu.Unlock()
	if h.timeoutBench == nil {
		h.timeoutBench = make(map[string]time.Time)
	}
	for k, until := range h.timeoutBench {
		if !until.After(now) {
			delete(h.timeoutBench, k)
		}
	}
	h.timeoutBench[comboBenchKey(comboName, entry)] = now.Add(grace)
}

// withoutBenchedModels drops models still inside their grace period. If that
// would leave nothing to try, the chain is returned unchanged: a combo whose
// every member is benched must keep answering rather than fail for minutes.
func (h *ChatHandler) withoutBenchedModels(comboName string, models []string, grace time.Duration) []string {
	if grace <= 0 {
		return models
	}
	now := time.Now()
	h.timeoutBenchMu.Lock()
	defer h.timeoutBenchMu.Unlock()
	if len(h.timeoutBench) == 0 {
		return models
	}
	kept := make([]string, 0, len(models))
	for _, m := range models {
		if until, ok := h.timeoutBench[comboBenchKey(comboName, m)]; ok && until.After(now) {
			continue
		}
		kept = append(kept, m)
	}
	if len(kept) == 0 {
		return models
	}
	return kept
}

func comboTimeoutError() *upstreamError {
	return &upstreamError{StatusCode: http.StatusGatewayTimeout, Body: []byte(`{"error":{"message":"model response timeout","type":"timeout_error","code":504}}`)}
}
