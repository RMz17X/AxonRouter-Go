package chat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/providers"
)

// hedgedResponseWriter wraps http.ResponseWriter to buffer response headers and initial chunks
// until a candidate claims victory (first HTTP 200). Once claimed, it commits headers and
// flushes streaming data directly to the underlying client writer.
type hedgedResponseWriter struct {
	master       http.ResponseWriter
	coord        *hedgeCoordinator
	candidateIdx int
	header       http.Header
	statusCode   int
	isWinner     bool
	committed    bool
	mu           sync.Mutex
}

func newHedgedResponseWriter(master http.ResponseWriter, coord *hedgeCoordinator, candidateIdx int) *hedgedResponseWriter {
	return &hedgedResponseWriter{
		master:       master,
		coord:        coord,
		candidateIdx: candidateIdx,
		header:       make(http.Header),
	}
}

func (hw *hedgedResponseWriter) Header() http.Header {
	return hw.header
}

func (hw *hedgedResponseWriter) WriteHeader(statusCode int) {
	hw.mu.Lock()
	defer hw.mu.Unlock()
	if hw.committed {
		return
	}
	hw.statusCode = statusCode
	if statusCode == http.StatusOK && hw.coord.commitOnWrite {
		// Queue-bound providers (TokenHarbor) send response headers long
		// before the first token — headers arrive in a ~60s admission
		// tick while content can lag another minute or never come
		// (measured 2026-11-09). Headers are not admission there: record
		// the status and let the first body Write claim the win, so the
		// client rides the first ticket that actually produces output.
		return
	}
	if statusCode == http.StatusOK {
		if hw.coord.claimWin(hw.candidateIdx) {
			hw.isWinner = true
			hw.committed = true
			for k, vv := range hw.header {
				for _, v := range vv {
					hw.master.Header().Add(k, v)
				}
			}
			hw.master.WriteHeader(statusCode)
			if f, ok := hw.master.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
}

func (hw *hedgedResponseWriter) Write(b []byte) (int, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()
	if !hw.committed {
		if hw.statusCode == 0 {
			hw.statusCode = http.StatusOK
		}
		if hw.statusCode == http.StatusOK {
			if hw.coord.claimWin(hw.candidateIdx) {
				hw.isWinner = true
				hw.committed = true
				for k, vv := range hw.header {
					for _, v := range vv {
						hw.master.Header().Add(k, v)
					}
				}
				hw.master.WriteHeader(hw.statusCode)
				if f, ok := hw.master.(http.Flusher); ok {
					f.Flush()
				}
			}
		}
	}
	if hw.isWinner {
		n, err := hw.master.Write(b)
		if f, ok := hw.master.(http.Flusher); ok {
			f.Flush()
		}
		return n, err
	}
	// Candidate lost or failed: silently discard writes so master stream remains pristine
	return len(b), nil
}

func (hw *hedgedResponseWriter) Flush() {
	hw.mu.Lock()
	defer hw.mu.Unlock()
	if hw.isWinner {
		if f, ok := hw.master.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func (hw *hedgedResponseWriter) Unwrap() http.ResponseWriter {
	return hw.master
}

// hedgeCoordinator tracks candidates racing to complete a request and coordinates
// immediate cancellation of losing candidates once a winner emerges.
type hedgeCoordinator struct {
	mu          sync.Mutex
	winner      int
	cancels     []context.CancelFunc
	wonChan     chan struct{}
	claimedOnce atomic.Bool
	// commitOnWrite defers the win from response headers to the first
	// body bytes (see hedgedResponseWriter.WriteHeader).
	commitOnWrite bool
}

func newHedgeCoordinator(n int) *hedgeCoordinator {
	return newHedgeCoordinatorMode(n, false)
}

func newHedgeCoordinatorMode(n int, commitOnWrite bool) *hedgeCoordinator {
	return &hedgeCoordinator{
		winner:        -1,
		cancels:       make([]context.CancelFunc, n),
		wonChan:       make(chan struct{}),
		commitOnWrite: commitOnWrite,
	}
}

func (c *hedgeCoordinator) setCancel(idx int, cancel context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancels[idx] = cancel
	if c.winner != -1 && c.winner != idx && cancel != nil {
		cancel()
	}
}

func (c *hedgeCoordinator) claimWin(idx int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.winner != -1 {
		return false
	}
	c.winner = idx
	if !c.claimedOnce.Swap(true) {
		close(c.wonChan)
	}
	// Cancel all losing candidates immediately to close their upstream connections
	for i, cancel := range c.cancels {
		if i != idx && cancel != nil {
			cancel()
		}
	}
	return true
}

func (c *hedgeCoordinator) getWinner() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.winner
}

type hedgeCandidateResult struct {
	idx      int
	connID   string
	connName string
	err      error
	duration time.Duration
}

// admissionTimeoutError marks a hedged request whose admission budget ran
// out before any candidate committed. It unwraps to the client-facing
// upstreamError (429, which the error masker turns into a retryable 503),
// so callers can distinguish "the client must retry now" from an ordinary
// fast candidate failure via isAdmissionTimeout.
type admissionTimeoutError struct {
	ue *upstreamError
}

func (e *admissionTimeoutError) Error() string { return e.ue.Error() }
func (e *admissionTimeoutError) Unwrap() error { return e.ue }

func isAdmissionTimeout(err error) bool {
	var ate *admissionTimeoutError
	return errors.As(err, &ate)
}

// hedgeAdmissionTimeout returns how long a hedged request may wait for a
// first candidate to commit before the gateway gives the client a
// retryable verdict instead of silence. Zero disables the budget (the
// standing behaviour for providers without a queue-bound pool).
//
// TokenHarbor's free tier is one global GPU queue per model: candidates
// are admitted slowly and silently, so the budget is a stall budget on
// the admission phase only — 60s without a winner means this draw is
// dead and the client retries with fresh tickets. It never applies after
// a winner commits; a flowing stream runs to completion.
func (h *ChatHandler) hedgeAdmissionTimeout(provider string) time.Duration {
	if providers.ResolveAlias(provider) == "tokenharbor" {
		return 60 * time.Second
	}
	return 0
}

// shouldHedgeProvider returns whether speculative multi-account hedging should be
// used for the given provider, along with the delay between candidates and attempt cap.
func (h *ChatHandler) shouldHedgeProvider(provider string) (enabled bool, delay time.Duration, maxAttempts int) {
	delay = 120 * time.Millisecond
	maxAttempts = 2

	// Dahl has hundreds of accounts and rapid microsecond/millisecond GPU queue fluctuations.
	// Speculative hedging enables hitting a freed concurrency slot without stalling the client.
	if provider == "dahl" {
		enabled = true
		delay = 80 * time.Millisecond
	}

	// TokenHarbor free models sit behind one global GPU queue per model:
	// a single ticket waits silently for tens of seconds and often never
	// lands, while a ladder of tickets across keys puts one of them at
	// the front far sooner (measured 2026-11-09: hedged TTFT 6–35s, 3/3,
	// vs the sequential path's 2/5 with a >150s hang). The pool is ~30K
	// accounts, so tickets are ammunition, not a scarce resource: 8
	// candidates staggered 2s apart, first commit wins. Owner directive:
	// client feel is the only metric; backend 429s are free collateral.
	if providers.ResolveAlias(provider) == "tokenharbor" {
		enabled = true
		delay = 2 * time.Second
		maxAttempts = 8
	}

	if h.Repo != nil {
		if settings, err := h.Repo.GetSettings(); err == nil && settings != nil {
			if strat, ok := settings.ProviderStrategies[provider]; ok {
				if strat.HedgeEnabled != nil {
					enabled = *strat.HedgeEnabled
				}
				if strat.HedgeDelayMs > 0 {
					delay = time.Duration(strat.HedgeDelayMs) * time.Millisecond
				}
				if strat.HedgeMaxAttempts > 0 {
					maxAttempts = strat.HedgeMaxAttempts
				}
			}
		}
	}
	return enabled, delay, maxAttempts
}

// pickHedgeCandidates filters active, non-locked connections for hedging.
func (h *ChatHandler) pickHedgeCandidates(allConns []*ProviderConnection, provider, model string, max int) []*ProviderConnection {
	var candidates []*ProviderConnection
	lockKey := canonicalLockModel(provider, model)
	for _, c := range allConns {
		if c == nil || c.IsActive != 1 {
			continue
		}
		if h.Repo != nil {
			locked, _ := h.Repo.IsConnectionModelLocked(c.ID, lockKey)
			if !locked && lockKey != model {
				locked, _ = h.Repo.IsConnectionModelLocked(c.ID, model)
			}
			if locked {
				continue
			}
		}
		candidates = append(candidates, c)
		if len(candidates) >= max {
			break
		}
	}
	return candidates
}

// executeHedged dispatches speculative candidates with staggered starts.
// If candidate 0 finishes quickly with 200 OK, candidate 1 is never launched.
// If candidate 0 fails early (e.g. 429), candidate 1 launches immediately.
// If candidate 0 lags beyond hedgeDelay, candidate 1 launches in parallel.
func (h *ChatHandler) executeHedged(
	ctx context.Context,
	w http.ResponseWriter,
	provider string,
	model string,
	requestedModel string,
	comboName string,
	body []byte,
	isStream bool,
	translateResponse bool,
	endpoint string,
	candidates []*ProviderConnection,
	hedgeDelay time.Duration,
	maxAttempts int,
	admissionTimeout time.Duration,
) error {
	if len(candidates) == 0 {
		return fmt.Errorf("no active connections for hedged provider: %s", provider)
	}
	if len(candidates) > maxAttempts {
		candidates = candidates[:maxAttempts]
	}

	numCandidates := len(candidates)
	resultsChan := make(chan hedgeCandidateResult, numCandidates)
	// An admission budget implies a queue-bound provider whose headers
	// precede output by a wide, useless margin: commit on first write.
	coord := newHedgeCoordinatorMode(numCandidates, admissionTimeout > 0)

	rootCtx, rootCancel := context.WithCancel(ctx)
	defer rootCancel()

	launch := func(idx int) {
		conn := candidates[idx]
		candCtx, candCancel := context.WithCancel(rootCtx)
		coord.setCancel(idx, candCancel)

		hw := newHedgedResponseWriter(w, coord, idx)

		go func() {
			start := time.Now()
			connObj, connData, err := h.getBestConnectionWithContext(candCtx, provider, conn.ID, nil, model)
			if err != nil || connObj == nil {
				resultsChan <- hedgeCandidateResult{
					idx:      idx,
					connID:   conn.ID,
					connName: connObjName(conn),
					err:      err,
					duration: time.Since(start),
				}
				return
			}

			fwdErr := h.tryForwardWithConnection(forwardRequestParams{
				Ctx:               candCtx,
				W:                 hw,
				Provider:          provider,
				Model:             model,
				RequestedModel:    requestedModel,
				ComboName:         comboName,
				Attempts:          idx + 1,
				ConnectionID:      connObj.ID,
				ConnName:          connObjName(connObj),
				ConnEmail:         connObjEmail(connObj),
				ConnData:          connData,
				Body:              body,
				IsStream:          isStream,
				TranslateResponse: translateResponse,
				Endpoint:          endpoint,
			})

			resultsChan <- hedgeCandidateResult{
				idx:      idx,
				connID:   connObj.ID,
				connName: connObjName(connObj),
				err:      fwdErr,
				duration: time.Since(start),
			}
		}()
	}

	// Launch primary candidate
	launch(0)
	nextToLaunch := 1
	activeRunning := 1

	timer := time.NewTimer(hedgeDelay)
	defer timer.Stop()

	// Admission budget: while no candidate has committed, total silence
	// is a stall. When the budget dies the whole hedge is cancelled and
	// the client gets a retryable verdict (see admissionTimeoutError)
	// instead of an open-ended hang. The budget disarms the moment a
	// winner commits — a flowing stream is never cut by it.
	var admissionTimer *time.Timer
	var admissionC <-chan time.Time
	if admissionTimeout > 0 {
		admissionTimer = time.NewTimer(admissionTimeout)
		admissionC = admissionTimer.C
		defer admissionTimer.Stop()
	}

	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-coord.wonChan:
			// A candidate won the race and committed HTTP 200.
			// Drain results until the winning candidate finishes streaming.
			for res := range resultsChan {
				if res.idx == coord.getWinner() {
					if res.err == nil {
						RecordAttemptFromContext(ctx, res.connID, res.connName, provider, model, 200, nil, res.duration.Milliseconds())
						return nil
					}
					return res.err
				}
			}
			return nil

		case res := <-resultsChan:
			activeRunning--
			if res.err == nil && coord.getWinner() == res.idx {
				RecordAttemptFromContext(ctx, res.connID, res.connName, provider, model, 200, nil, res.duration.Milliseconds())
				return nil
			}

			lastErr = res.err
			RecordAttemptFromContext(ctx, res.connID, res.connName, provider, model, 0, res.err, res.duration.Milliseconds())
			h.recordHedgeCandidateFailure(ctx, provider, model, res.connID, res.err)

			// If failure is model-scoped (e.g. context overflow, unsupported media/vision,
			// or cluster-wide concurrency cap), all candidates on this provider/model will fail
			// identically. Fast-fail immediately so outer combo advances to next model without delay.
			var ue *upstreamError
			if errors.As(res.err, &ue) {
				if providers.IsModelFitError(ue.StatusCode, ue.Body) ||
					providers.IsUnsupportedMediaError(ue.StatusCode, ue.Body) ||
					providers.IsModelConcurrencyError(ue.StatusCode, ue.Body) {
					rootCancel()
					return res.err
				}
			}

			// If no winner yet, launch next candidate immediately instead of waiting for timer
			if coord.getWinner() == -1 && nextToLaunch < numCandidates {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(hedgeDelay)
				launch(nextToLaunch)
				nextToLaunch++
				activeRunning++
			} else if activeRunning == 0 && coord.getWinner() == -1 {
				// All launched candidates exhausted and none won
				return lastErr
			}

		case <-admissionC:
			if coord.getWinner() != -1 {
				// Raced with a win: the budget no longer applies.
				admissionC = nil
				continue
			}
			rootCancel()
			return &admissionTimeoutError{ue: &upstreamError{
				StatusCode: http.StatusTooManyRequests,
				Body:       []byte(`{"error":{"message":"Upstream admission timed out: no candidate produced output within the admission budget, please retry","type":"server_error","code":"upstream_unavailable"}}`),
			}}

		case <-timer.C:
			// Candidate is taking longer than hedgeDelay: fire speculative hedge in parallel
			if coord.getWinner() == -1 && nextToLaunch < numCandidates {
				log.InfoCtx(ctx, "hedging", "speculative candidate launched in parallel",
					"provider", provider, "model", model, "candidate", nextToLaunch)
				launch(nextToLaunch)
				nextToLaunch++
				activeRunning++
				timer.Reset(hedgeDelay)
			}
		}
	}
}

// executeHedgedWaves runs hedged waves over the provider's connections
// until one candidate commits, the candidate pool is walked, or the
// provider's shared admission budget runs out. A wave whose candidates
// all fail fast (instant 429s from a saturated queue) is followed
// immediately by the next wave of fresh connections — the tickets are
// ammunition — while a wave of silently queued candidates consumes the
// admission budget and ends the request with a retryable verdict.
// Used for TokenHarbor, whose free-tier queue makes a single sequential
// draw both slow and unreliable.
func (h *ChatHandler) executeHedgedWaves(
	ctx context.Context,
	w http.ResponseWriter,
	provider string,
	model string,
	requestedModel string,
	comboName string,
	body []byte,
	isStream bool,
	translateResponse bool,
	endpoint string,
	allConns []*ProviderConnection,
	hedgeDelay time.Duration,
	maxAttempts int,
) error {
	budget := h.hedgeAdmissionTimeout(provider)
	var deadline time.Time
	if budget > 0 {
		deadline = time.Now().Add(budget)
	}
	tried := make(map[string]bool, len(allConns))
	var lastErr error
	for wave := 0; ; wave++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		remaining := make([]*ProviderConnection, 0, len(allConns))
		for _, c := range allConns {
			if c != nil && !tried[c.ID] {
				remaining = append(remaining, c)
			}
		}
		candidates := h.pickHedgeCandidates(remaining, provider, model, maxAttempts)
		if len(candidates) == 0 {
			if lastErr != nil {
				return lastErr
			}
			// Nothing was ever launched (e.g. every connection is
			// model-locked): never report success for a request nobody
			// served — return a real error so the caller falls through
			// to the sequential path's lock/wait semantics.
			return &upstreamError{
				StatusCode: http.StatusServiceUnavailable,
				Body:       []byte(`{"error":{"message":"no eligible upstream connections available, please retry","type":"server_error","code":"upstream_unavailable"}}`),
			}
		}
		for _, c := range candidates {
			tried[c.ID] = true
		}
		var waveTimeout time.Duration
		if budget > 0 {
			waveTimeout = time.Until(deadline)
			if waveTimeout <= 0 {
				return &admissionTimeoutError{ue: &upstreamError{
					StatusCode: http.StatusTooManyRequests,
					Body:       []byte(`{"error":{"message":"Upstream admission timed out: no candidate produced output within the admission budget, please retry","type":"server_error","code":"upstream_unavailable"}}`),
				}}
			}
		}
		log.InfoCtx(ctx, "hedging", "hedged wave dispatch",
			"provider", provider, "model", model, "wave", wave, "candidates", len(candidates))
		err := h.executeHedged(ctx, w, provider, model, requestedModel, comboName, body, isStream, translateResponse, endpoint, candidates, hedgeDelay, maxAttempts, waveTimeout)
		if err == nil {
			return nil
		}
		if isAdmissionTimeout(err) {
			return err
		}
		// Identical-failure classes: every remaining candidate would
		// fail the same way, so stop instead of walking the pool.
		var ue *upstreamError
		if errors.As(err, &ue) && (providers.IsModelFitError(ue.StatusCode, ue.Body) ||
			providers.IsUnsupportedMediaError(ue.StatusCode, ue.Body) ||
			providers.IsModelConcurrencyError(ue.StatusCode, ue.Body)) {
			return err
		}
		lastErr = err
		if budget <= 0 {
			// No admission budget: a single wave is the whole policy
			// (the standing behaviour for every other provider).
			return lastErr
		}
	}
}

// recordHedgeCandidateFailure updates locks or error records for a failing hedge candidate.
func (h *ChatHandler) recordHedgeCandidateFailure(ctx context.Context, provider, model, connID string, err error) {
	if err == nil || connID == "" {
		return
	}
	var ue *upstreamError
	if errors.As(err, &ue) {
		if providers.IsModelConcurrencyError(ue.StatusCode, ue.Body) {
			cooldownSec := 30
			lockKey := canonicalLockModel(provider, model)
			if h.Repo != nil {
				_ = h.Repo.LockConnectionModel(connID, lockKey, cooldownSec, 1)
				_ = h.Repo.RecordConnectionScopedError(connID, model, "chat", ue.StatusCode, extractErrorText(ue.Body), 1)
			}
			log.WarnCtx(ctx, "hedging", "candidate concurrency saturated, locking model on connection",
				"conn", connID, "provider", provider, "model", model, "cooldownSec", cooldownSec)
			return
		}
		if ue.StatusCode == http.StatusTooManyRequests {
			if h.Repo != nil {
				_ = h.Repo.RecordConnectionScopedError(connID, model, "chat", ue.StatusCode, extractErrorText(ue.Body), 1)
			}
		}
	}
}
