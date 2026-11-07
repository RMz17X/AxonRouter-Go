package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/providers"
)

// Egress IP auditing for the routing log, fragment 980.
//
// The usage and failure lines already name the egress path (egress.go:
// pool name, or "direct") and the connection (conn=…), but a pool NAME
// does not say which address the request actually left from — and for a
// rotating residential pool the address is the whole point: a provider
// that rate-limits or blocks by IP cannot be diagnosed from a pool
// label, and an account whose path silently moved to a different egress
// mid-day is exactly the drift the anti-suspend work hunts. So each
// entry now also carries the path's last-known egress IP, plus an
// egress_changed marker (old->new) when a connection's path IP differs
// from the one its previous entry carried.
//
// The IP is discovered by an echo lookup THROUGH the connection's own
// client — the same proxy/relay/direct transport the request itself
// used — against the echo targets the dashboard's exit-node probe
// already uses. Lookups are cached per path for egressIPTTL and
// refreshed asynchronously: the request path only ever reads the
// last-known value and never waits on an echo. Both caches are
// in-memory only; a restart forgets the previous IPs, so the first
// entry after a boot carries no egress_changed marker (there is
// nothing honest to compare against).

// egressIPTTL is how long one path's discovered egress IP is trusted
// before a background refresh re-checks it.
const egressIPTTL = 10 * time.Minute

// egressIPRetry is the re-check delay after a failed lookup, so a dead
// path is not re-probed on every request.
const egressIPRetry = time.Minute

// egressEchoTargets are the IPv4 echo endpoints, the same list the
// dashboard's exit-node probe uses (proxypools_exitnode.go).
var egressEchoTargets = []string{
	"http://v4.ident.me",
	"http://whatismyip.akamai.com",
	"https://ipv4.icanhazip.com",
}

type egressIPEntry struct {
	ip         string
	nextTry    time.Time
	refreshing bool
}

// egressIPState holds the per-path IP cache and the per-connection
// last-logged IP the change marker compares against.
var egressIPState = struct {
	sync.Mutex
	byPath map[string]*egressIPEntry
	byConn map[string]string
}{
	byPath: make(map[string]*egressIPEntry),
	byConn: make(map[string]string),
}

// egressPathKey is the cache identity of one egress path: the resolved
// pool's id, a relay's host, a short hash of a legacy proxy target, or
// "direct". A proxy URL is never used raw — it can carry credentials,
// and the key is a log-adjacent value.
func egressPathKey(eg Egress, connData *ConnectionData) string {
	poolID := eg.PoolID
	if connData != nil && connData.ResolvedProxyPoolID != "" {
		poolID = connData.ResolvedProxyPoolID
	}
	switch {
	case poolID != "" && poolID != "direct" && poolID != "__none__":
		return "pool:" + poolID
	case eg.Kind == "relay" && eg.Target != "":
		if u, err := url.Parse(eg.Target); err == nil && u.Host != "" {
			return "relay:" + u.Host
		}
		return "relay:" + egressTargetHash(eg.Target)
	case eg.Target != "":
		return "proxy:" + egressTargetHash(eg.Target)
	default:
		return "direct"
	}
}

func egressTargetHash(target string) string {
	sum := sha256.Sum256([]byte(target))
	return hex.EncodeToString(sum[:6])
}

// fillEgressIP stamps a usage record with the egress IP fields its log
// line carries: egress_ip (the path's last-known address) and
// egress_changed (old->new, only when this connection's path IP moved
// since its previous entry). It never blocks: a missing or stale IP
// kicks off a background refresh and the line carries whatever is
// known right now.
func (h *ChatHandler) fillEgressIP(info *UsageLogInfo, connData *ConnectionData, cfg *providers.ProviderConfig) {
	if info == nil || info.ConnectionID == "" {
		return
	}
	info.EgressIP, info.EgressChanged = h.egressIPSnapshot(connData, cfg, info.ConnectionID)
}

// egressIPSnapshot returns the (last-known IP, change marker) pair for
// one connection's path, applying the change bookkeeping exactly once
// per call — callers that log one attempt through several lines take
// the pair once and reuse it, so a change is marked on the attempt that
// observed it and not re-marked by its sibling lines.
func (h *ChatHandler) egressIPSnapshot(connData *ConnectionData, cfg *providers.ProviderConfig, connectionID string) (string, string) {
	if connectionID == "" {
		return "", ""
	}
	eg := resolveEgress(connData, cfg)
	key := egressPathKey(eg, connData)

	egressIPState.Lock()
	entry := egressIPState.byPath[key]
	if entry == nil {
		entry = &egressIPEntry{}
		egressIPState.byPath[key] = entry
	}
	ip := entry.ip
	refresh := !entry.refreshing && time.Now().After(entry.nextTry)
	if refresh {
		entry.refreshing = true
	}
	changed := ""
	if ip != "" {
		if prev := egressIPState.byConn[connectionID]; prev != "" && prev != ip {
			changed = prev + "->" + ip
		}
		egressIPState.byConn[connectionID] = ip
	}
	egressIPState.Unlock()

	if refresh {
		go h.refreshEgressIP(key, connData)
	}
	return ip, changed
}

// refreshEgressIP re-discovers one path's egress IP through the
// connection's own client and updates the cache. Any failure keeps the
// last-known IP and only moves the next try out by egressIPRetry: a
// path that cannot answer an echo right now is retried quietly, never
// hammered per request.
func (h *ChatHandler) refreshEgressIP(key string, connData *ConnectionData) {
	found := ""
	func() {
		var cp *ConnectionData
		if connData != nil {
			dup := *connData
			cp = &dup
		}
		client, err := h.getClientForConnection(cp)
		if err != nil || client == nil {
			return
		}
		probe := *client
		probe.Timeout = 8 * time.Second
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		for _, target := range egressEchoTargets {
			if ip := fetchEgressEcho(ctx, &probe, target); ip != "" {
				found = ip
				break
			}
		}
	}()

	egressIPState.Lock()
	if entry := egressIPState.byPath[key]; entry != nil {
		entry.refreshing = false
		if found != "" {
			entry.ip = found
			entry.nextTry = time.Now().Add(egressIPTTL)
		} else {
			entry.nextTry = time.Now().Add(egressIPRetry)
		}
	}
	egressIPState.Unlock()
	if found != "" {
		log.Debug("egress", "egress ip discovered", "path", key, "ip", found)
	}
}

// fetchEgressEcho reads one echo endpoint and returns the IPv4 address
// it answered with, or "" for anything that is not a clean IPv4 reply.
func fetchEgressEcho(ctx context.Context, client *http.Client, target string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 128))
	ipStr := strings.TrimSpace(string(body))
	parsed := net.ParseIP(ipStr)
	if parsed == nil || parsed.To4() == nil {
		return ""
	}
	return ipStr
}
