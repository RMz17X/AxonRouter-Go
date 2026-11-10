package chat

import (
	"context"
	"crypto/sha256"
	"sync"
)

// Per-request cache for applyTokenSavers output. The fallback chain runs the
// savers inside every connection attempt, but an attempt's input is the one
// client body the whole request shares: on a large payload (100K+ tokens)
// each attempt otherwise re-runs the full RTK filter pipeline, the injection
// scan and the prompt injections over identical bytes. During a provider
// 429 storm — dozens of attempts per request, hedged attempts in parallel —
// that redundant compression was the gateway's top CPU burn (measured
// 150–320% container CPU on 2026-11-10). The savers are a pure function of
// (body, claudeNative, settings), so one computation per request and body
// shape is enough; attempts reuse it.
type tokenSaverCacheKey struct {
	bodyHash     [sha256.Size]byte
	claudeNative bool
}

type tokenSaverCacheEntry struct {
	body        []byte
	origTokens  int
	savedTokens int
	savedPct    int
}

type tokenSaverCache struct {
	mu      sync.Mutex
	entries map[tokenSaverCacheKey]tokenSaverCacheEntry
}

type tokenSaverCacheCtxKey struct{}

// withTokenSaverCache installs the per-request cache on ctx if absent.
func withTokenSaverCache(ctx context.Context) context.Context {
	if ctx == nil || tokenSaverCacheFrom(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, tokenSaverCacheCtxKey{}, &tokenSaverCache{
		entries: map[tokenSaverCacheKey]tokenSaverCacheEntry{},
	})
}

func tokenSaverCacheFrom(ctx context.Context) *tokenSaverCache {
	if ctx == nil {
		return nil
	}
	if c, ok := ctx.Value(tokenSaverCacheCtxKey{}).(*tokenSaverCache); ok {
		return c
	}
	return nil
}

func (c *tokenSaverCache) get(key tokenSaverCacheKey) (tokenSaverCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	return e, ok
}

func (c *tokenSaverCache) put(key tokenSaverCacheKey, e tokenSaverCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = e
}
