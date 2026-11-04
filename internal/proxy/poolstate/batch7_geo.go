package poolstate

import "time"

// GeoPoolCount reports how many pools currently hold a geo cache entry
// (batch 7: GET /api/system/memory's inMemory.geoPools, the counterpart of
// upstream's Object.keys(poolGeoSnapshot()).length). Expired entries are
// pruned first, matching GetPoolGeo's TTL behaviour.
func GeoPoolCount() int {
	geoMu.Lock()
	defer geoMu.Unlock()
	now := time.Now().UnixMilli()
	for poolID, entry := range geoCache {
		if entry.TS+geoTTL.Milliseconds() < now {
			delete(geoCache, poolID)
		}
	}
	return len(geoCache)
}
