package executor

import (
	"strconv"
	"strings"

	"axonrouter/proxy/internal/providers"
)

// The free tier's reasoning models spend their output budget on chain-of-thought
// before emitting a visible answer. A client that asks for a small max_tokens —
// a probe, a title, a one-word reply — therefore gets finish_reason "length"
// with empty content and no error: the whole budget went to thinking. The
// reference bridge measured this on the same upstream (obf token-budget.js):
// 50 and 100 tokens are empty, 256 is empty for big-pickle, 512 is truncated,
// and 2048 plus thinking headroom completes.
//
// The accepted fix is an additive budget: a floor for the visible answer plus a
// fixed reasoning reserve, clamped to the model's own output ceiling so the
// request never asks for more than the model can emit.
const (
	openCodeMinAnswerTokens  = 1024
	openCodeThinkingBudget   = 4096
	openCodeMinPlainTokens   = 512
	openCodeMaxOutputCeiling = 131072
)

// effectiveOpenCodeMaxTokens raises a client's output budget into the range
// where a reasoning model still has room for a visible answer. A request that
// already asks for more is never lowered below its own value, only clamped to
// the model ceiling.
func effectiveOpenCodeMaxTokens(requested int, provider, model string, reasoning bool) int {
	if requested <= 0 {
		return 0
	}
	effective := requested
	if reasoning {
		if effective < openCodeMinAnswerTokens {
			effective = openCodeMinAnswerTokens
		}
		effective += openCodeThinkingBudget
	} else if effective < openCodeMinPlainTokens {
		effective = openCodeMinPlainTokens
	}
	ceiling := openCodeModelOutputCeiling(provider, model)
	if effective > ceiling {
		effective = ceiling
	}
	return effective
}

// openCodeModelOutputCeiling resolves the model's own output ceiling. The
// capability table is authoritative: its MaxOutput is copied verbatim from
// upstream's catalog. The substring limit table is consulted only when it
// reports a ceiling large enough to hold the reasoning floor — its 4096-token
// default for unknown ids is a guess, and clamping a reasoning request to it
// would reintroduce the empty answer this floor exists to prevent.
func openCodeModelOutputCeiling(provider, model string) int {
	if caps := providers.GetCapabilitiesForModel(provider, model); caps.MaxOutput > 0 {
		return min(caps.MaxOutput, openCodeMaxOutputCeiling)
	}
	if _, maxOut := providers.GetModelTokenLimits(model); maxOut > openCodeMinAnswerTokens+openCodeThinkingBudget {
		return min(maxOut, openCodeMaxOutputCeiling)
	}
	return openCodeMaxOutputCeiling
}

// openCodeIsReasoningRequest reports whether the request targets a model that
// thinks before answering, or asks for reasoning explicitly.
func openCodeIsReasoningRequest(reqMap map[string]any, provider, model string) bool {
	if raw, ok := reqMap["reasoning_effort"].(string); ok {
		effort := strings.ToLower(strings.TrimSpace(raw))
		if effort != "" && effort != "none" {
			return true
		}
	}
	return providers.GetCapabilitiesForModel(provider, model).Reasoning
}

// openCodeRequestedMaxTokens reads the client's output budget in either
// spelling. The CLI sends max_completion_tokens; older clients send max_tokens.
func openCodeRequestedMaxTokens(reqMap map[string]any) (int, bool) {
	for _, key := range []string{"max_completion_tokens", "max_tokens"} {
		raw, ok := reqMap[key]
		if !ok || raw == nil {
			continue
		}
		switch v := raw.(type) {
		case float64:
			if v > 0 {
				return int(v), true
			}
		case int:
			if v > 0 {
				return v, true
			}
		case int64:
			if v > 0 {
				return int(v), true
			}
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				return n, true
			}
		}
	}
	return 0, false
}

// applyOpenCodeTokenFloor rewrites the output budget into the CLI's spelling
// (max_completion_tokens) and raises it when it is too small to survive the
// model's reasoning phase. A request that declared no budget keeps the
// upstream default: the floor corrects an explicit small value, it does not
// invent one.
func applyOpenCodeTokenFloor(reqMap map[string]any, provider, model string) {
	requested, ok := openCodeRequestedMaxTokens(reqMap)
	if !ok {
		return
	}
	effective := effectiveOpenCodeMaxTokens(requested, provider, model, openCodeIsReasoningRequest(reqMap, provider, model))
	if effective <= 0 {
		return
	}
	delete(reqMap, "max_tokens")
	reqMap["max_completion_tokens"] = effective
}
