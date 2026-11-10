package dashboard

import (
	"sync"
	"time"
)

// claimStatusTTL bounds how long a MiniMax daily-claim status read is
// reused. The quota page asks once per visible MiniMax row; without this
// cache every page pass would fan out into one live upstream read per
// account. A successful claim (single or bulk) invalidates immediately,
// so the TTL only ever covers the read-only path.
const claimStatusTTL = 5 * time.Minute

type claimStatusCacheEntry struct {
	status  MiniMaxSignInStatus
	expires time.Time
}

var (
	claimStatusCacheMu sync.Mutex
	claimStatusCache   = make(map[string]claimStatusCacheEntry)
)

func cachedClaimStatus(connID string) (MiniMaxSignInStatus, bool) {
	claimStatusCacheMu.Lock()
	defer claimStatusCacheMu.Unlock()
	entry, ok := claimStatusCache[connID]
	if !ok {
		return MiniMaxSignInStatus{}, false
	}
	if time.Now().After(entry.expires) {
		delete(claimStatusCache, connID)
		return MiniMaxSignInStatus{}, false
	}
	return entry.status, true
}

func storeClaimStatus(connID string, status MiniMaxSignInStatus) {
	claimStatusCacheMu.Lock()
	defer claimStatusCacheMu.Unlock()
	claimStatusCache[connID] = claimStatusCacheEntry{
		status:  status,
		expires: time.Now().Add(claimStatusTTL),
	}
}

// invalidateClaimStatus drops one connection's cached status, or the
// whole cache when connID is empty.
func invalidateClaimStatus(connID string) {
	claimStatusCacheMu.Lock()
	defer claimStatusCacheMu.Unlock()
	if connID == "" {
		claimStatusCache = make(map[string]claimStatusCacheEntry)
		return
	}
	delete(claimStatusCache, connID)
}
