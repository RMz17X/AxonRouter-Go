package schedule

import (
	"context"
	json "encoding/json/v2"
	"math/rand/v2"
	"strings"
	"time"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlers/chat"
	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/models"
)

// Idle generation prober, fragment 979.
//
// Fragment 973 made ToS-disable detection decisive but traffic-driven:
// the chat lane classifies generation responses as they pass, so an
// account Google disabled while it sat idle is only caught when its next
// real request draws the verdict — mid-rotation, in front of a user.
// This prober sends ONE tiny generation request through the same lane
// (ChatHandler.PingConnection: pinned connection, probe context) for
// every Antigravity account that has been quiet past the configured
// threshold, so the verdict lands on a background tick instead. The
// response flows through the lane's existing classification untouched —
// a ToS 403 disables the account exactly as live traffic would, a 429
// parks it exactly as live traffic would, and a transport failure
// changes nothing at all.
//
// Cost, stated plainly: each probe is one real generation call ("hi",
// one output token) against the account's quota. With the derived
// defaults (6h idle threshold, 1h per-account floor) a permanently idle
// account is probed at most ~4 times a day; an account with any real
// traffic is probed rarely or never, because traffic keeps resetting
// its idle clock.

// AntigravityIdleProbeModel is the generation lane the probe exercises:
// the cheapest broadly-entitled flash tier in the catalog. The verdict
// that matters (ToS disable) is account-shaped, not model-shaped, so the
// lightest lane is the right surface.
const AntigravityIdleProbeModel = "gemini-3.8-flash-low"

const (
	// idleProbeProvider is the only provider in the prober's scope: the
	// disable blindness it closes is Antigravity's. The settings
	// resolver can enable other providers, but no other provider's
	// executor verdicts feed a disable path, so probing them would burn
	// quota for a liveness signal nobody consumes.
	idleProbeProvider = "antigravity"

	// idleProbeTick is the sweep cadence. The per-account settings
	// (threshold, interval) decide who is due; the tick only bounds how
	// late a due probe can land.
	idleProbeTick = time.Minute

	// idleProbeFirstDelay is the first sweep's offset after boot, so a
	// restart verifies long-idle accounts promptly instead of a full
	// tick later — and not in the same instant as every other boot
	// loop.
	idleProbeFirstDelay = 15 * time.Second

	// idleProbeMaxPerTick bounds one sweep's work. Due accounts beyond
	// the cap wait for the next tick; the shuffle makes the wait fair.
	idleProbeMaxPerTick = 8
)

// IdleProber sweeps idle Antigravity accounts and probes them through
// the chat lane. Its only state is the per-account last-probe map, held
// in memory on purpose: a restart forgetting it can cost at most one
// early re-probe per account, and the idle threshold still has to be
// met before that can happen.
type IdleProber struct {
	repo  *db.Repo
	chatH *chat.ChatHandler

	lastProbe map[string]time.Time
}

// NewIdleProber builds the prober. Either dependency missing makes Run a
// no-op: there is nothing to probe with, or nowhere to probe from.
func NewIdleProber(repo *db.Repo, chatH *chat.ChatHandler) *IdleProber {
	return &IdleProber{repo: repo, chatH: chatH, lastProbe: map[string]time.Time{}}
}

// Run sweeps until the context ends. The first sweep runs after a short
// delay, then once per tick.
func (p *IdleProber) Run(ctx context.Context) {
	if p == nil || p.repo == nil || p.chatH == nil {
		return
	}
	first := time.NewTimer(idleProbeFirstDelay)
	select {
	case <-ctx.Done():
		first.Stop()
		return
	case <-first.C:
	}
	p.sweep(ctx)

	ticker := time.NewTicker(idleProbeTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.sweep(ctx)
		}
	}
}

// sweep probes every due account once, sequentially (concurrency 1: a
// burst of simultaneous generations is both a quota spike and the
// machine pattern the anti-suspend work exists to avoid), with a
// jittered pause between probes so the sweep itself is staggered rather
// than a metronome.
func (p *IdleProber) sweep(ctx context.Context) {
	cfg := p.repo.IdleProbeConfigFor(idleProbeProvider)
	if !cfg.Enabled {
		return
	}
	conns, err := p.repo.GetProviderConnections(idleProbeProvider, false)
	if err != nil {
		log.Warn("idleprobe", "list connections failed", "error", err)
		return
	}
	now := time.Now()
	due := make([]*models.ProviderConnection, 0, len(conns))
	for _, conn := range conns {
		if p.due(conn, cfg, now) {
			due = append(due, conn)
		}
	}
	if len(due) == 0 {
		return
	}
	rand.Shuffle(len(due), func(i, j int) { due[i], due[j] = due[j], due[i] })
	if len(due) > idleProbeMaxPerTick {
		due = due[:idleProbeMaxPerTick]
	}
	for i, conn := range due {
		if ctx.Err() != nil {
			return
		}
		if i > 0 {
			pause := time.Duration(rand.Int64N(int64(10*time.Second))) + 3*time.Second
			select {
			case <-ctx.Done():
				return
			case <-time.After(pause):
			}
		}
		p.probe(ctx, conn)
	}
}

// due reports whether one connection should be probed now: active, not
// probed inside the per-account interval, quiet past the threshold, and
// not stored-disabled.
func (p *IdleProber) due(conn *models.ProviderConnection, cfg db.IdleProbeConfig, now time.Time) bool {
	if conn == nil || conn.IsActive != 1 {
		return false
	}
	if last, ok := p.lastProbe[conn.ID]; ok && now.Sub(last) < cfg.Interval {
		return false
	}
	if now.Sub(lastActivity(conn)) < cfg.Threshold {
		return false
	}
	if storedDisabled(conn.Data) {
		// Fragment 973's guard: a disabled account is never dialed, by
		// any surface, for any reason. It stays out until the deliberate
		// post-appeal reset. DisableConnectionWithReason also clears
		// isActive, so this check is for the row that carries the marker
		// while active — the resurrection shape — where isActive alone
		// would let the probe through. The skip is stamped into the
		// probe map so the log line repeats at most once per interval,
		// not once per tick.
		log.Info("idleprobe", "skipping stored-disabled connection", "conn", conn.ID)
		p.lastProbe[conn.ID] = now
		return false
	}
	return true
}

// probe sends the one generation request. The attempt is recorded
// before the call: a probe that fails upstream still counts for the
// per-account interval, so a dead-egress account is not re-probed every
// tick. Fail-open: the returned error is logged and dropped — the only
// state changes a probe may cause are the ones the chat lane's own
// classification makes from the upstream's actual response.
func (p *IdleProber) probe(ctx context.Context, conn *models.ProviderConnection) {
	p.lastProbe[conn.ID] = time.Now()
	body, err := buildPingBody(idleProbeProvider, AntigravityIdleProbeModel)
	if err != nil {
		log.Warn("idleprobe", "build probe body failed", "conn", conn.ID, "error", err)
		return
	}
	log.Info("idleprobe", "probing idle connection", "conn", conn.ID, "model", AntigravityIdleProbeModel)
	pingCtx, cancel := context.WithTimeout(ctx, PingTimeout)
	defer cancel()
	if err := p.chatH.PingConnection(pingCtx, idleProbeProvider, AntigravityIdleProbeModel, conn.ID, body); err != nil {
		log.Warn("idleprobe", "probe failed", "conn", conn.ID, "error", err)
	}
}

// lastActivity is the connection's last generation-traffic instant: the
// round-robin stamp every served request writes. A never-used
// connection falls back to its creation time. An unreadable stamp reads
// as the zero time — infinitely idle — because the probe is precisely
// the tool that verifies an account whose bookkeeping is suspect.
func lastActivity(conn *models.ProviderConnection) time.Time {
	for _, raw := range []string{deref(conn.LastUsedAt), conn.CreatedAt} {
		if raw == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

// storedDisabled mirrors the dashboard's storedDisabledMessage predicate
// (internal/handlers/dashboard/connection_probe.go): the verdict
// DisableConnectionWithReason persists in the connection blob's status /
// testStatus fields. isActive alone does not carry it (an operator
// toggle is not a disable), so the blob is checked explicitly.
func storedDisabled(data string) bool {
	if data == "" {
		return false
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return false
	}
	status, _ := raw["status"].(string)
	testStatus, _ := raw["testStatus"].(string)
	return strings.EqualFold(status, "disabled") || strings.EqualFold(testStatus, "disabled")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
