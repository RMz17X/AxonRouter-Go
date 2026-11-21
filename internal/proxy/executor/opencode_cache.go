package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"axonrouter/proxy/internal/proxy"
)

// opencodePromptCacheKey derives the prompt-cache key the official CLI sends.
//
// The CLI reuses one key per conversation so the upstream KV cache survives
// across turns: `promptCacheKey: /^ses_[0-9a-f]{64}$/.test(T) ? T.slice(4) : T`,
// capped at 64 characters (obf lib/server/messages.js:357-363). The gateway's
// canonical session is 30 characters, so the cap never truncates it — the
// helper exists for caller-supplied sessions that arrive in the 68-character
// shape.
func opencodePromptCacheKey(session string) string {
	if session == "" {
		return ""
	}
	key := session
	if len(session) == 68 && strings.HasPrefix(session, "ses_") && isLowerHex(session[4:]) {
		key = session[4:]
	}
	if len(key) > 64 {
		key = key[:64]
	}
	return key
}

func isLowerHex(s string) bool {
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// applyOpenCodeCacheFields installs the two fields the CLI sends for prompt
// cache reuse. `store:false` keeps the request stateless, and
// `prompt_cache_key` names the KV-cache lane. Without the key, a long
// conversation is re-prefilled from token zero on every turn; the reference
// bridge measured 5-9 s cold versus <1.5 s warm on a 100K-token prompt
// (obf docs/features/SMART-PROXY-ROUTING.md).
//
// The key must equal the session the request travels on: the gate meters and
// caches per session, and a key that names a different session than
// x-opencode-session re-prefills while claiming a cache hit.
func applyOpenCodeCacheFields(reqMap map[string]any, session string) {
	if key := opencodePromptCacheKey(session); key != "" {
		reqMap["prompt_cache_key"] = key
	}
	reqMap["store"] = false
}

// opencodeCacheFieldsRejected inspects a 400 answer for a refused prompt-cache
// field and reports which fields to drop. The body is read and put back, so a
// caller that is not a cache-field refusal still surfaces the original answer.
func opencodeCacheFieldsRejected(resp *http.Response) (cache, store bool) {
	if resp == nil || resp.StatusCode != http.StatusBadRequest || resp.Body == nil {
		return false, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return false, false
	}
	return opencodeCacheFieldsRejectedBody(body)
}

// opencodeCacheFieldsRejectedBody classifies the body of a 400. The gate does
// not name every rejected field, so a generic "unsupported field" drops both —
// the same recovery the reference bridge applies
// (obf lib/server/chat-attempt-core.js:163).
func opencodeCacheFieldsRejectedBody(body []byte) (cache, store bool) {
	lower := strings.ToLower(string(body))
	if !strings.Contains(lower, "unsupported field") &&
		!strings.Contains(lower, "unknown field") &&
		!strings.Contains(lower, "unrecognized field") &&
		!strings.Contains(lower, "unsupported parameter") {
		return false, false
	}
	hasCache := strings.Contains(lower, "prompt_cache_key")
	hasStore := strings.Contains(lower, "store")
	if hasCache || hasStore {
		return hasCache, hasStore
	}
	return true, true
}

// forwardOpenCodeWithCacheFields sends the request and, when the gate answers
// 400 because it does not accept a cache field, drops the refused fields and
// replays the request exactly once.
//
// One replay, never a loop: a second 400 means the refusal was about something
// else, and repeating it only delays the caller's actionable error. reqMap is
// the map body was marshaled from; a nil map means the body was never parsed
// and no cache field could have been added.
//
// The 400 arrives in either position. forwardOpenCode converts every non-200
// into an *proxy.UpstreamError, so the response is normally nil and the body
// lives on the error; both shapes are classified.
func forwardOpenCodeWithCacheFields(
	ctx context.Context,
	body []byte,
	reqMap map[string]any,
	send func(body []byte) (*http.Response, error),
	sendDirect func(body []byte) (*http.Response, error),
	rotate func(),
) (*http.Response, error) {
	sendAll := func(payload []byte) (*http.Response, error) {
		return forwardOpenCodeRotating(ctx,
			func() (*http.Response, error) { return send(payload) },
			func() (*http.Response, error) { return sendDirect(payload) },
			rotate)
	}

	resp, err := sendAll(body)
	if reqMap == nil {
		return resp, err
	}
	var cache, store bool
	switch {
	case err != nil:
		var ue *proxy.UpstreamError
		if !errors.As(err, &ue) || ue.StatusCode != http.StatusBadRequest {
			return nil, err
		}
		cache, store = opencodeCacheFieldsRejectedBody(ue.Body)
	case resp != nil:
		cache, store = opencodeCacheFieldsRejected(resp)
	}
	if !cache && !store {
		return resp, err
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if cache {
		delete(reqMap, "prompt_cache_key")
	}
	if store {
		delete(reqMap, "store")
	}
	stripped, marshalErr := marshalStable(reqMap)
	if marshalErr != nil {
		return nil, fmt.Errorf("strip opencode cache fields: %w", marshalErr)
	}
	return sendAll(stripped)
}
