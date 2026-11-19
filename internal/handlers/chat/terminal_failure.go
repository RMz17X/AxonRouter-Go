package chat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/log"
)

// terminalFailureClass classifies a failure that the gateway could not
// translate into a client-facing upstream error.
//
// The classification exists because the terminal log line used to carry only
// a status and a duration. A 502 is the gateway's own status for "the
// fallback layer gave up", and the several reasons behind it — no usable
// connection, a pinned connection that was ineligible, an exhausted combo, a
// provider marked unhealthy — need different operator actions. Without the
// class, the 2026-11-18 incident left 96 of 245 chat 502s unattributable.
type terminalFailureClass string

const (
	// failureClassNoConnection: the provider had no connection to dial.
	failureClassNoConnection terminalFailureClass = "no_connection"
	// failureClassProviderUnhealthy: the provider was skipped as unhealthy or
	// its model was marked broken before anything was sent.
	failureClassProviderUnhealthy terminalFailureClass = "provider_unhealthy"
	// failureClassPinnedIneligible: a pinned connection could not be used and
	// the strategy that was supposed to replace it produced no candidate.
	failureClassPinnedIneligible terminalFailureClass = "pinned_ineligible"
	// failureClassComboExhausted: every member of the combo failed.
	failureClassComboExhausted terminalFailureClass = "combo_exhausted"
	// failureClassFirstByteTimeout: the upstream did not produce a first byte
	// within the request's budget.
	failureClassFirstByteTimeout terminalFailureClass = "first_byte_timeout"
	// failureClassUpstreamFailure: the upstream answered with a failure the
	// gateway could not map to an upstreamError.
	failureClassUpstreamFailure terminalFailureClass = "upstream_failure"
	// failureClassUnknown: the failure carried no recognisable marker.
	failureClassUnknown terminalFailureClass = "unknown"
)

// classifyTerminalFailure assigns a class to a terminal failure.
//
// The markers are the message fragments the fallback layer writes today. They
// are matched rather than restructured on purpose: the failure strings are
// part of what the operator reads in the log, and changing them to carry a
// typed value would rewrite every message that names them.
func classifyTerminalFailure(err error) terminalFailureClass {
	if err == nil {
		return failureClassUnknown
	}
	if errors.Is(err, ErrFirstByteTimeout) {
		return failureClassFirstByteTimeout
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "did not produce a first byte"),
		strings.Contains(msg, "first byte"),
		strings.Contains(msg, "first-byte"):
		return failureClassFirstByteTimeout
	case strings.Contains(msg, "no active connections"),
		strings.Contains(msg, "no available connections"):
		return failureClassNoConnection
	case strings.Contains(msg, "is unhealthy"),
		strings.Contains(msg, "model-locked"),
		strings.Contains(msg, "unavailable"):
		return failureClassProviderUnhealthy
	case strings.Contains(msg, "pinned connection"),
		strings.Contains(msg, "ineligible"):
		return failureClassPinnedIneligible
	case strings.Contains(msg, "all combo models failed"),
		strings.Contains(msg, "no valid entries"):
		return failureClassComboExhausted
	default:
		return failureClassUpstreamFailure
	}
}

// logTerminalUpstreamFailure writes the structured line that makes a terminal
// gateway failure attributable.
//
// It reports the provider and model the request ran on, the class of the
// failure, whether the failure came from an upstream status the gateway could
// not mask, and whether the response had already been committed. The
// committed flag matters: a failure after the commit cannot become a status
// code any more, so it reaches the client as a truncated body instead.
//
// No secret is logged: the provider, the model, the class and the error text
// are all request metadata, and the error text comes from the gateway's own
// failure paths rather than from an upstream body.
func logTerminalUpstreamFailure(ctx context.Context, endpoint, provider, model string, err error, committed bool) {
	if err == nil {
		return
	}
	kv := []any{
		"endpoint", endpoint,
		"provider", provider,
		"model", model,
		"class", string(classifyTerminalFailure(err)),
		"committed", committed,
		"error", err.Error(),
	}
	// The upstream status, when the failure carries one, is the single most
	// useful field: it separates "the upstream said 429" from "the gateway
	// had nowhere to send the request".
	var ue *upstreamError
	if errors.As(err, &ue) {
		kv = append(kv, "upstreamStatus", ue.StatusCode)
	}
	if budget := firstByteBudgetFromContext(ctx); !budget.deadline.IsZero() {
		kv = append(kv, "waitedMs", budget.waited().Milliseconds())
	}
	log.ErrorCtx(ctx, "upstream", "terminal upstream failure", kv...)
}

// writeTerminalFailure writes the response for a failure the gateway could
// not translate into an upstream error.
//
// A first-byte timeout gets the OpenAI-compatible error shape, because the
// clients of this API parse the body: a bare text body would crash a client
// that expects the error envelope. The wait is reported in a header, so a
// client can tell a slow upstream from a stuck one without parsing the
// message.
//
// Everything else is a 503. The status used to be 502, which says "the
// upstream answered and the answer was bad" — but on this path nothing
// answered: the gateway had no connection to send the request to, the
// provider was unhealthy, the combo was exhausted, or the upstream failed in
// a way the gateway could not mask. A 503 says the service is temporarily
// unavailable, and every client of this API treats it as transient and
// retries it, which is the outcome the gateway wants: the next attempt can
// land on a different account.
//
// No Retry-After is published here. The wait is the client's own to choose,
// and the gateway has no honest answer to give: it does not know when the
// upstream will recover, and a delay it invented would only defer the retry
// the client should make immediately.
//
// The status change is invisible to a client that was already handling a
// 502 correctly, because the body keeps the same error envelope.
func writeTerminalFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrFirstByteTimeout) || (err != nil && (strings.Contains(strings.ToLower(err.Error()), "did not produce a first byte") || strings.Contains(strings.ToLower(err.Error()), "first-byte timeout"))) {
		var fe *firstByteDeadlineError
		waitedMs := int64(0)
		if errors.As(err, &fe) {
			waitedMs = fe.waited.Milliseconds()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Axon-First-Byte-Wait-Ms", strconv.FormatInt(waitedMs, 10))
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream did not respond in time","type":"timeout_error","code":504}}`))
		return
	}
	handlerutil.WriteJSONError(w, http.StatusServiceUnavailable, fmt.Sprintf("upstream error: %v", err))
}
