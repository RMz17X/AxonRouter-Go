package db

import (
	"context"
	"strings"
	"time"

	"9router/proxy/internal/speedlayer"
)

// speed.go is the Repo's bridge to the cluster speed layer (P3). The
// Repo carries the store the app installed at boot; a nil store (tests,
// tooling, any boot that never wired the layer) makes every helper a
// no-op, and the database reads underneath remain the behaviour — the
// layer is a shared cache in front of the truth, never a replacement
// for it.
//
// What rides the layer today: the per-model cooldown locks. The
// selector used to run one SELECT per candidate connection to answer
// "is this model locked on this account"; the writer now also
// publishes the lock to the speed layer, and the reader consults it
// first. The account-scoped cooldowns (rateLimitedUntil /
// oauthLockedUntil) deliberately do NOT ride it: the selector already
// reads those from the connection rows it loaded fresh from the shared
// database for this very request, so they are cluster-true by
// construction and an overlay could only add a hop.

// SetSpeedStore installs the speed layer the overlay helpers consult.
// Called once at boot from the app wiring, after the store is built.
func (r *Repo) SetSpeedStore(s speedlayer.Store) {
	r.speed.Store(s)
}

func (r *Repo) speedStore() speedlayer.Store {
	if r == nil {
		return nil
	}
	if v := r.speed.Load(); v != nil {
		if s, ok := v.(speedlayer.Store); ok {
			return s
		}
	}
	return nil
}

// modelLockOverlayKey namespaces one (connection, model) lock. The
// separator is a unit separator: connection ids and model names are
// operator/provider strings that never contain it.
func modelLockOverlayKey(connID, model string) string {
	return "modellock:" + connID + "\x1f" + model
}

// publishModelLock mirrors a freshly written model lock into the speed
// layer. Best-effort by design: a failed publish costs a database read
// on the next selection, never a wrong answer — the row already holds
// the lock, and the overlay is only ever consulted as a positive
// shortcut (see overlayModelLocked).
func (r *Repo) publishModelLock(connID, model string, until time.Time) {
	s := r.speedStore()
	if s == nil {
		return
	}
	ttl := time.Until(until)
	if ttl <= 0 {
		return
	}
	ctx, cancel := speedlayer.OpContext(context.Background())
	defer cancel()
	_ = s.Set(ctx, modelLockOverlayKey(connID, model), until.UTC().Format(time.RFC3339), ttl)
}

// overlayModelLocked reports whether the speed layer holds a live lock
// for (connID, model). It only ever answers "locked" — a miss, an
// expired entry, or a layer error all report false and let the caller
// fall through to the database read, so the overlay can shorten the
// hot path but can never change its answer.
func (r *Repo) overlayModelLocked(connID, model string) bool {
	s := r.speedStore()
	if s == nil {
		return false
	}
	ctx, cancel := speedlayer.OpContext(context.Background())
	defer cancel()
	raw, ok, err := s.Get(ctx, modelLockOverlayKey(connID, model))
	if err != nil || !ok {
		return false
	}
	until, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return until.After(time.Now())
}

// deleteModelLockOverlay drops one (connection, model) lock from the
// layer (the single-model clear in UnlockConnectionModel).
func (r *Repo) deleteModelLockOverlay(connID, model string) {
	s := r.speedStore()
	if s == nil {
		return
	}
	ctx, cancel := speedlayer.OpContext(context.Background())
	defer cancel()
	_ = s.Delete(ctx, modelLockOverlayKey(connID, model))
}

// clearModelLockOverlay drops every model lock the layer holds for a
// connection (the clear-all path in ClearConnectionModelLocks).
func (r *Repo) clearModelLockOverlay(connID string) {
	s := r.speedStore()
	if s == nil {
		return
	}
	ctx, cancel := speedlayer.OpContext(context.Background())
	defer cancel()
	_, _ = s.DeletePrefix(ctx, "modellock:"+connID+"\x1f")
}
