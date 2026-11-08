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
}

func newHedgeCoordinator(n int) *hedgeCoordinator {
	return &hedgeCoordinator{
		winner:  -1,
		cancels: make([]context.CancelFunc, n),
		wonChan: make(chan struct{}),
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
) error {
	if len(candidates) == 0 {
		return fmt.Errorf("no active connections for hedged provider: %s", provider)
	}
	if len(candidates) > maxAttempts {
		candidates = candidates[:maxAttempts]
	}

	numCandidates := len(candidates)
	resultsChan := make(chan hedgeCandidateResult, numCandidates)
	coord := newHedgeCoordinator(numCandidates)

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
