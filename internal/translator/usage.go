package translator

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type pendingFragment struct {
	data      []byte
	createdAt time.Time
}

var (
	statesMu sync.Mutex
	states   = make(map[string]*StreamState)
	// pendingJSON holds the tail of a truncated SSE JSON payload, keyed by
	// sessionKey, until the continuation chunk arrives. Some upstreams (opencode
	// free-tier) split a single JSON object across multiple SSE events.
	pendingJSON = make(map[string]pendingFragment)
	// promptEstimates holds a body-size token estimate per sessionKey, seeded
	// before the stream starts. It stands in for the client-facing input
	// token count when the upstream never reports usage at all (a chat
	// upstream that ignores stream_options.include_usage), so the client's
	// context meter still moves and auto-compact can fire. Estimates are not
	// usage: they never enter state.Usage or the analytics path.
	promptEstimates = make(map[string]promptEstimate)
)

type promptEstimate struct {
	tokens    int
	createdAt time.Time
}

// SeedStreamPromptEstimate records the request-body token estimate for a
// session before its first chunk is translated. A non-positive estimate is
// "unknown" and is ignored.
func SeedStreamPromptEstimate(sessionKey string, tokens int) {
	if sessionKey == "" || tokens <= 0 {
		return
	}
	statesMu.Lock()
	defer statesMu.Unlock()
	promptEstimates[sessionKey] = promptEstimate{tokens: tokens, createdAt: time.Now()}
}

// promptEstimateFor returns and forgets the seeded estimate for a session.
func promptEstimateFor(sessionKey string) int {
	if sessionKey == "" {
		return 0
	}
	est, ok := promptEstimates[sessionKey]
	if !ok {
		return 0
	}
	delete(promptEstimates, sessionKey)
	return est.tokens
}

// maxPendingJSON bounds a single buffered fragment so a hostile/endless
// stream cannot grow the map without limit.
const maxPendingJSON = 1 << 20 // 1 MiB

// sessionCounter makes every stream session key unique.
//
// time.Now().UnixNano() alone is not a unique id on Windows: the wall clock
// there ticks at ~15.6 ms, so 200 concurrent requests observed only two
// distinct nanosecond values — 104 of them shared one. Requests that shared a
// key also shared one *StreamState, and the translator mutates that state's
// maps outside the lock, which crashed the gateway with "concurrent map
// writes" under load.
var sessionCounter atomic.Uint64

// sessionNow is the clock NewSessionKey stamps keys with. It is a variable so
// a test can pin it: with a coarse clock the counter is the only thing that
// keeps two keys apart, and no CI platform can supply that clock on demand.
var sessionNow = time.Now

// NewSessionKey returns a key that no other live call can produce. The
// counter makes it unique within the process; the timestamp keeps it sortable
// and lets the stale-state pruner work on age.
func NewSessionKey(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, sessionNow().UnixNano(), sessionCounter.Add(1))
}

// isTruncatedJSON reports whether err is json.Unmarshal failing because the
// input ended mid-value ("unexpected end of JSON input") — i.e. a valid JSON
// prefix awaiting a continuation, as opposed to actual malformed data.
func isTruncatedJSON(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "unexpected end of JSON input") || strings.Contains(s, "unexpected EOF")
}

func pruneStaleStatesLocked() {
	if len(states) < 50 && len(pendingJSON) < 50 && len(promptEstimates) < 50 {
		return
	}
	now := time.Now()
	for k, v := range states {
		if v == nil || v.CreatedAt.IsZero() || now.Sub(v.CreatedAt) > 10*time.Minute {
			delete(states, k)
			delete(pendingJSON, k)
		}
	}
	for k, f := range pendingJSON {
		if f.createdAt.IsZero() || now.Sub(f.createdAt) > 10*time.Minute {
			delete(pendingJSON, k)
		}
	}
	for k, e := range promptEstimates {
		if e.createdAt.IsZero() || now.Sub(e.createdAt) > 10*time.Minute {
			delete(promptEstimates, k)
		}
	}
}

// GetStreamUsage returns the accumulated usage for a stream session and removes it.
func GetStreamUsage(sessionKey string) *OpenAIUsage {
	statesMu.Lock()
	defer statesMu.Unlock()
	pruneStaleStatesLocked()
	delete(pendingJSON, sessionKey)
	delete(promptEstimates, sessionKey)
	if state, ok := states[sessionKey]; ok {
		delete(states, sessionKey)
		if state.Usage != nil {
			usage := *state.Usage
			return &usage
		}
	}
	return nil
}

// ClearStreamState removes a stream session state and any buffered fragment.
func ClearStreamState(sessionKey string) {
	statesMu.Lock()
	defer statesMu.Unlock()
	pruneStaleStatesLocked()
	delete(states, sessionKey)
	delete(pendingJSON, sessionKey)
	delete(promptEstimates, sessionKey)
}

// Global last-usage capture for the non-streaming path.
// Deprecated: use SetUsage and GetAndClearUsage with context instead.
var lastUsage *OpenAIUsage
var lastUsageMu sync.Mutex

// SetLastUsage stores usage from a completed stream translation.
// Deprecated: use SetUsage instead.
func SetLastUsage(u *OpenAIUsage) {
	lastUsageMu.Lock()
	defer lastUsageMu.Unlock()
	lastUsage = u
}

// GetAndClearLastUsage returns and clears the stored last usage.
// Deprecated: use GetAndClearUsage instead.
func GetAndClearLastUsage() *OpenAIUsage {
	lastUsageMu.Lock()
	defer lastUsageMu.Unlock()
	u := lastUsage
	lastUsage = nil
	return u
}

// requestedModelCtxKey stores the original client-requested model for echoing in streaming responses (PR #3693).
type requestedModelCtxKey struct{}

func WithRequestedModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, requestedModelCtxKey{}, model)
}

func RequestedModelFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(requestedModelCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// SeedStreamState pre-seeds a stream session with the requested model so message_start
// echoes the client's model instead of the provider's chunk model (decolua/9router#3693).
func SeedStreamState(sessionKey, model string) {
	if sessionKey == "" || model == "" {
		return
	}
	statesMu.Lock()
	defer statesMu.Unlock()
	pruneStaleStatesLocked()
	if state, ok := states[sessionKey]; ok {
		if state.Model == "" {
			state.Model = model
		}
		return
	}
	states[sessionKey] = &StreamState{
		CreatedAt:      time.Now(),
		MessageId:      "",
		Model:          model,
		ToolCalls:      make(map[int]ToolCallState),
		ToolArgBuffers: make(map[int]string),
	}
}

// usageCtxKey is the context key for per-request usage storage.
type usageCtxKey struct{}

// usageHolder stores usage per-request in context.
type usageHolder struct {
	mu    sync.Mutex
	usage *OpenAIUsage
}

// WithUsageCapture returns a new context that captures usage per-request.
func WithUsageCapture(ctx context.Context) context.Context {
	return context.WithValue(ctx, usageCtxKey{}, &usageHolder{})
}

// SetUsage stores usage in the request context. Falls back to global if context has no holder.
func SetUsage(ctx context.Context, u *OpenAIUsage) {
	if ctx != nil {
		if h, ok := ctx.Value(usageCtxKey{}).(*usageHolder); ok {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.usage = u
			return
		}
	}
	// Fallback to global for backward compatibility
	SetLastUsage(u)
}

// GetAndClearUsage retrieves and clears usage from the request context (or global fallback if no holder).
func GetAndClearUsage(ctx context.Context) *OpenAIUsage {
	if ctx != nil {
		if h, ok := ctx.Value(usageCtxKey{}).(*usageHolder); ok {
			h.mu.Lock()
			defer h.mu.Unlock()
			u := h.usage
			h.usage = nil
			return u
		}
	}
	return GetAndClearLastUsage()
}

// promptEstimateCtxKey carries the request-body token estimate from the
// handler that owns the request bytes to whichever relay seeds the stream
// session (handlers/chat/forward.go and the executor relays).
type promptEstimateCtxKey struct{}

// WithPromptEstimate stores a pre-computed input-token estimate on the request
// context. Zero means "no estimate" and is the natural zero value.
func WithPromptEstimate(ctx context.Context, tokens int) context.Context {
	return context.WithValue(ctx, promptEstimateCtxKey{}, tokens)
}

// PromptEstimateFromContext returns the seeded estimate, or 0 when none.
func PromptEstimateFromContext(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	if v, ok := ctx.Value(promptEstimateCtxKey{}).(int); ok {
		return v
	}
	return 0
}

var (
	base64Marker     = []byte(";base64,")
	claudeDataMarker = []byte(`"data":`)
)

// EstimatePromptTokens approximates a request body's prompt-token count
// from its size: roughly four bytes per token over the body's text,
// excluding inline base64 media payloads. A base64 image's byte length
// says nothing about its token cost (an image is ~1-2K tokens at any
// resolution), so counting it would inflate the estimate and make the
// usage floor fabricate prompt tokens for image turns; skipping the
// request entirely — the old behaviour — left every image-carrying
// history unestimated, so a segment-counting upstream's prompt total
// passed through unfloored. Two payload shapes are recognised: data
// URIs (";base64," followed by the payload) in any API dialect, and
// Claude-native media blocks ("data": "<payload>").
func EstimatePromptTokens(body []byte) int {
	return (len(body) - base64PayloadBytes(body) + 3) / 4
}

func base64PayloadBytes(body []byte) int {
	total := 0
	for i := 0; i < len(body); {
		j := bytes.Index(body[i:], base64Marker)
		if j < 0 {
			break
		}
		start := i + j + len(base64Marker)
		n := base64Run(body, start)
		total += n
		i = start + n
	}
	for i := 0; i < len(body); {
		j := bytes.Index(body[i:], claudeDataMarker)
		if j < 0 {
			break
		}
		k := i + j + len(claudeDataMarker)
		for k < len(body) && (body[k] == ' ' || body[k] == '\t') {
			k++
		}
		if k >= len(body) || body[k] != '"' {
			i = k
			continue
		}
		n := base64Run(body, k+1)
		if n >= 100 {
			total += n
		}
		i = k + 1 + n
	}
	return total
}

// base64Run returns the length of the base64 alphabet run starting at i.
func base64Run(body []byte, i int) int {
	n := 0
	for j := i; j < len(body); j++ {
		c := body[j]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '+' || c == '/' || c == '=' || c == '-' || c == '_' {
			n++
			continue
		}
		break
	}
	return n
}
