package usagetracker

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/speedlayer"
	"axonrouter/proxy/internal/translator"
)

const (
	pendingTimeout = 60 * time.Second
	ringCap        = 50
)

// ActiveRequest matches the Next.js dashboard activeRequests item shape.
type ActiveRequest struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Account  string `json:"account"`
	Count    int    `json:"count"`
	// RequestedModel is the name the client asked for, and ComboName is set
	// only when a combo served the request. A combo request reports the member
	// that serves it in Model/Provider, so the dashboard can draw
	// axonrouter-go -> combo -> member instead of showing the member as if the
	// caller had asked for it by name. ComboName is the explicit signal: the
	// map must not have to guess which names are combos.
	RequestedModel string `json:"requestedModel,omitempty"`
	ComboName      string `json:"comboName,omitempty"`
}

// RecentRequest matches the Next.js dashboard recentRequests item shape.
type RecentRequest struct {
	Timestamp        string `json:"timestamp"`
	Model            string `json:"model"`
	Provider         string `json:"provider"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	CachedTokens     int    `json:"cachedTokens"`
	Status           string `json:"status"`
	RequestedModel   string `json:"requestedModel,omitempty"`
	ComboName        string `json:"comboName,omitempty"`
	// Endpoint is the gateway route the request arrived on (e.g.
	// /v1/chat/completions). The Realtime table derives the request's
	// capability from it, the way AxonRouter's live rows do.
	Endpoint string `json:"endpoint,omitempty"`
	// APIKey is the upstream credential the request used, ALWAYS in the
	// masked form maskAPIKeyForStats produces — a raw key never rides this
	// struct. Empty when the request carried no credential.
	APIKey string `json:"apiKey,omitempty"`
	// Account is the connection/account display value the request-details
	// rows resolve (see detailListItem): the stored account label where one
	// exists, else the connection id, else "Default".
	Account string `json:"account,omitempty"`
	// IsStream reports whether the client asked for a streamed response. It is
	// only set where the push site knows the request's streaming nature;
	// history rows do not record it, so seeded entries leave it false.
	IsStream bool `json:"isStream,omitempty"`
	// LatencyMs is how long the request took, recorded for completed requests
	// only: a refused member has a rejection time, not a latency, and the map
	// must not present one as the other.
	LatencyMs   int64  `json:"latencyMs,omitempty"`
	LoopWarning string `json:"loopWarning,omitempty"`
	Proxy       string `json:"proxy,omitempty"`
	Egress      string `json:"egress,omitempty"`
}

// StreamPayload represents the payload sent over SSE on /api/usage/stream.
type StreamPayload struct {
	ActiveRequests []ActiveRequest `json:"activeRequests"`
	RecentRequests []RecentRequest `json:"recentRequests"`
	ErrorProvider  string          `json:"errorProvider"`
	Pending        PendingState    `json:"pending"`
}

// PendingState represents in-flight counts by model and account.
type PendingState struct {
	ByModel   map[string]int            `json:"byModel"`
	ByAccount map[string]map[string]int `json:"byAccount"`
}

// The tracker's live state — in-flight counters, the names behind them,
// the recent-request ring, and the last-error marker — lives in the
// cluster speed layer (P3), not in this process. Two nodes serving one
// cluster therefore show the same active requests on either dashboard
// (§6 drill item 2), and a node restart loses nothing the layer held.
// What stays in-process is only what is meaningless off this node: the
// SSE subscribers (they are this node's own HTTP clients) and the
// broadcast debounce.
//
// Speed-layer key layout:
//
//	pending/model/<modelKey>            counter
//	pending/acct/<connKey>\x1f<modelKey> counter
//	pending/names/<modelKey>            JSON pendingNames
//	usage/recent                        ring of JSON RecentRequest
//	usage/lasterror                     JSON lastError, 15 s TTL
const (
	modelCounterPrefix = "pending/model/"
	acctCounterPrefix  = "pending/acct/"
	namesPrefix        = "pending/names/"
	recentRingKey      = "usage/recent"
	lastErrorKey       = "usage/lasterror"
	acctKeySep         = "\x1f"
)

// Tracker tracks in-flight requests, recent completions, and broadcasts SSE updates.
type Tracker struct {
	// own is this tracker's private store, used until boot configures
	// the shared one through SetStore. Production wiring always
	// configures it; tests and tooling that construct a Tracker
	// directly keep the pre-P3 per-instance behaviour.
	own speedlayer.Store

	mu                sync.Mutex
	subscribers       map[chan []byte]struct{}
	broadcastDebounce *time.Timer
	lastRepo          *db.Repo
	ringSeeded        bool
}

// configuredStore is the speed layer boot installed for the tracker,
// resolved on every call so a Tracker constructed before the wiring
// runs still lands on the shared store.
var configuredStore atomic.Pointer[speedlayer.Store]

// SetStore installs the shared speed-layer store the tracker reads and
// writes. Called once from the app wiring (P3).
func SetStore(s speedlayer.Store) {
	if s != nil {
		configuredStore.Store(&s)
	}
}

func (t *Tracker) store() speedlayer.Store {
	if s := configuredStore.Load(); s != nil {
		return *s
	}
	return t.own
}

var globalTracker *Tracker
var once sync.Once

// GetTracker returns the singleton UsageTracker instance.
func GetTracker() *Tracker {
	once.Do(func() {
		globalTracker = NewTracker()
	})
	return globalTracker
}

// NewTracker creates a new Tracker instance.
func NewTracker() *Tracker {
	return &Tracker{
		own:         speedlayer.NewMemoryStore(),
		subscribers: make(map[chan []byte]struct{}),
	}
}

// pendingNames are the names behind one in-flight request: what the client
// asked for, and the combo that served it when one did.
type pendingNames struct {
	Requested string `json:"r"`
	Combo     string `json:"c"`
}

// lastError is the speed-layer form of the last failed provider.
type lastError struct {
	Provider string `json:"p"`
	Ts       int64  `json:"ts"`
}

// TrackPending updates the in-flight count for a given model, provider, and connection.
func (t *Tracker) TrackPending(model, provider, connectionID string, started bool, isError bool) {
	t.TrackPendingRequested("", "", model, provider, connectionID, started, isError)
}

// TrackPendingRequested additionally records the names the client asked for and
// the combo that served it. A combo request asks for the combo and is served by
// a member, so both names describe one in-flight request: they share the same
// counter and key, and they ride along for as long as that counter is above
// zero.
func (t *Tracker) TrackPendingRequested(requested, combo, model, provider, connectionID string, started bool, isError bool) {
	modelKey := model
	if provider != "" {
		modelKey = fmt.Sprintf("%s (%s)", model, provider)
	}
	connKey := connectionID
	if connKey == "" {
		connKey = "__direct__"
	}
	delta := int64(1)
	if !started {
		delta = -1
	}

	ctx, cancel := speedlayer.OpContext(context.Background())
	defer cancel()
	st := t.store()

	newModelCount, err := st.AddCounter(ctx, modelCounterPrefix+modelKey, delta)
	if err != nil {
		// The layer is best-effort dashboard state: a dropped update
		// during a total store outage coincides with the database being
		// down (the degradation backend lives in it), when the gateway
		// is not serving anyway. The failover store logs the outage
		// itself; one line here keeps the drop visible without a
		// per-request flood (Debug, not Warn).
		log.Debug("usagetracker", "pending counter update dropped", "err", err)
		return
	}
	acctKey := acctCounterPrefix + connKey + acctKeySep + modelKey
	if _, err := st.AddCounter(ctx, acctKey, delta); err != nil {
		log.Debug("usagetracker", "pending account counter update dropped", "err", err)
	}

	// Pair the names with the counter. Started sets them, zero clears them: a
	// stale pairing would keep calling a finished combo request a combo.
	if started && newModelCount > 0 && (requested != "" || combo != "") {
		if encoded, err := json.Marshal(pendingNames{Requested: requested, Combo: combo}); err == nil {
			_ = st.Set(ctx, namesPrefix+modelKey, string(encoded), pendingTimeout*10)
		}
	}
	if newModelCount <= 0 {
		_ = st.Delete(ctx, namesPrefix+modelKey)
		_ = st.Delete(ctx, modelCounterPrefix+modelKey)
	}

	if !started && isError && provider != "" {
		if encoded, err := json.Marshal(lastError{Provider: provider, Ts: time.Now().UnixMilli()}); err == nil {
			_ = st.Set(ctx, lastErrorKey, string(encoded), 15*time.Second)
		}
	}

	t.scheduleBroadcast(nil)
}

// PushRecent adds a completed request to the ring buffer and notifies subscribers.
func (t *Tracker) PushRecent(req RecentRequest, repo *db.Repo) {
	t.ensureRingSeeded(repo)

	ctx, cancel := speedlayer.OpContext(context.Background())
	defer cancel()
	if encoded, err := json.Marshal(req); err == nil {
		if err := t.store().RingPush(ctx, recentRingKey, string(encoded), ringCap); err != nil {
			log.Debug("usagetracker", "recent ring push dropped", "err", err)
		}
	}

	t.scheduleBroadcast(repo)
}

// GetRecent returns a copy of the recent requests ring buffer.
func (t *Tracker) GetRecent() []RecentRequest {
	ctx, cancel := speedlayer.OpContext(context.Background())
	defer cancel()
	raw, err := t.store().RingRead(ctx, recentRingKey, ringCap)
	if err != nil {
		return []RecentRequest{}
	}
	out := make([]RecentRequest, 0, len(raw))
	for _, entry := range raw {
		var req RecentRequest
		if err := json.Unmarshal([]byte(entry), &req); err == nil {
			out = append(out, req)
		}
	}
	return out
}

// ensureRingSeeded seeds the speed layer's ring from DB history once, when
// the ring is empty. In a cluster the first node to serve a PushRecent
// seeds it for every node; the check is the ring itself, so later nodes
// see it non-empty and skip. A seeding race between two fresh nodes can
// duplicate entries — the payload builder dedupes recent rows anyway.
func (t *Tracker) ensureRingSeeded(repo *db.Repo) {
	if repo == nil {
		return
	}
	t.mu.Lock()
	if t.ringSeeded {
		t.mu.Unlock()
		return
	}
	t.ringSeeded = true
	t.mu.Unlock()

	ctx, cancel := speedlayer.OpContext(context.Background())
	defer cancel()
	st := t.store()
	if existing, err := st.RingRead(ctx, recentRingKey, 1); err == nil && len(existing) > 0 {
		return
	}
	// The ring is a best-effort seed: a failed read leaves it empty and live
	// pushes refill it. That is the right behaviour, but it must not be silent
	// — an empty ring is otherwise indistinguishable from a fresh install.
	rows, err := repo.GetRecentUsageHistory(ringCap)
	if err != nil {
		log.Warn("usage ring seed failed", "err", err)
		return
	}
	// The ring is newest-first and RingPush prepends, so history is
	// pushed oldest-first to end in the stored order.
	for i := len(rows) - 1; i >= 0; i-- {
		if encoded, err := json.Marshal(recentFromHistoryRow(rows[i])); err == nil {
			_ = st.RingPush(ctx, recentRingKey, string(encoded), ringCap)
		}
	}
}

// GetActiveState computes the current active state for SSE streaming.
func (t *Tracker) GetActiveState(repo *db.Repo) StreamPayload {
	t.ensureRingSeeded(repo)
	return t.buildPayload(repo)
}

func (t *Tracker) buildPayload(repo *db.Repo) StreamPayload {
	ctx, cancel := speedlayer.OpContext(context.Background())
	defer cancel()
	st := t.store()

	modelCounts, _ := st.Counters(ctx, modelCounterPrefix)
	acctCounts, _ := st.Counters(ctx, acctCounterPrefix)

	byModel := make(map[string]int, len(modelCounts))
	for k, v := range modelCounts {
		if v <= 0 {
			continue // a finish without a start on this store; never report it
		}
		byModel[strings.TrimPrefix(k, modelCounterPrefix)] = int(v)
	}
	byAccount := make(map[string]map[string]int, len(acctCounts))
	for k, v := range acctCounts {
		if v <= 0 {
			continue
		}
		rest := strings.TrimPrefix(k, acctCounterPrefix)
		connKey, modelKey, found := strings.Cut(rest, acctKeySep)
		if !found {
			continue
		}
		inner, ok := byAccount[connKey]
		if !ok {
			inner = make(map[string]int)
			byAccount[connKey] = inner
		}
		inner[modelKey] = int(v)
	}

	// Names for every model key with live counts, in one round trip.
	nameKeys := make([]string, 0, len(byModel))
	for modelKey := range byModel {
		nameKeys = append(nameKeys, namesPrefix+modelKey)
	}
	names := make(map[string]pendingNames, len(nameKeys))
	if raw, err := st.GetMany(ctx, nameKeys); err == nil {
		for k, v := range raw {
			var pn pendingNames
			if err := json.Unmarshal([]byte(v), &pn); err == nil {
				names[strings.TrimPrefix(k, namesPrefix)] = pn
			}
		}
	}

	// Build connection name map
	connMap := make(map[string]string)
	if repo != nil && len(byAccount) > 0 {
		var connIDs []string
		for connID := range byAccount {
			if connID != "" && connID != "__direct__" {
				connIDs = append(connIDs, connID)
			}
		}
		if len(connIDs) > 0 {
			if m, err := repo.GetConnectionNames(context.Background(), connIDs); err == nil {
				connMap = m
			}
		}
	}
	var active []ActiveRequest
	accountCountedModels := make(map[string]int)
	for connID, models := range byAccount {
		accName := connMap[connID]
		if connID == "__direct__" || connID == "" {
			accName = "Public / Direct"
		} else if accName == "" {
			if len(connID) > 8 {
				accName = fmt.Sprintf("Account %s...", connID[:8])
			} else {
				accName = "Account " + connID
			}
		}
		for modelKey, count := range models {
			if count > 0 {
				mName, pName := parseModelKey(modelKey)
				accountCountedModels[modelKey] += count
				active = append(active, ActiveRequest{
					Model:          mName,
					Provider:       pName,
					Account:        accName,
					Count:          count,
					RequestedModel: names[modelKey].Requested,
					ComboName:      names[modelKey].Combo,
				})
			}
		}
	}

	// If any active requests were in byModel but not captured in byAccount, include them as Public / Direct
	for modelKey, totalCount := range byModel {
		alreadyCounted := accountCountedModels[modelKey]
		remaining := totalCount - alreadyCounted
		if remaining > 0 {
			mName, pName := parseModelKey(modelKey)
			active = append(active, ActiveRequest{
				Model:          mName,
				Provider:       pName,
				Account:        "Public / Direct",
				Count:          remaining,
				RequestedModel: names[modelKey].Requested,
				ComboName:      names[modelKey].Combo,
			})
		}
	}

	recent := make([]RecentRequest, 0, 20)
	seen := make(map[string]bool)
	for _, r := range t.GetRecent() {
		if r.PromptTokens == 0 && r.CompletionTokens == 0 {
			continue
		}
		min := ""
		if len(r.Timestamp) >= 16 {
			min = r.Timestamp[:16]
		}
		k := fmt.Sprintf("%s|%s|%d|%d|%s", r.Model, r.Provider, r.PromptTokens, r.CompletionTokens, min)
		if !seen[k] {
			seen[k] = true
			recent = append(recent, r)
			if len(recent) >= 20 {
				break
			}
		}
	}

	errProv := ""
	if raw, ok, err := st.Get(ctx, lastErrorKey); err == nil && ok {
		var le lastError
		if err := json.Unmarshal([]byte(raw), &le); err == nil {
			if time.Now().UnixMilli()-le.Ts < 10000 {
				errProv = le.Provider
			}
		}
	}

	return StreamPayload{
		ActiveRequests: active,
		RecentRequests: recent,
		ErrorProvider:  errProv,
		Pending: PendingState{
			ByModel:   byModel,
			ByAccount: byAccount,
		},
	}
}

// maskRecentAPIKey masks a stored usageHistory apiKey for the recent-requests
// wire shape. It is maskAPIKeyForStats (package handlers) re-implemented on
// handlerutil: the tracker must not import handlers, and the two masks must
// agree or the same row would show different key forms on the stats and SSE
// surfaces. usageHistory rows written by this build already hold
// handlerutil.MaskAPIKey output (re-masking that form is a no-op); rows the
// Next.js dashboard wrote can hold the full key, which is why the mask is
// applied here at all.
func maskRecentAPIKey(key string) string {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" || trimmed == "local-no-key" || trimmed == "***" {
		return ""
	}
	if handlerutil.RuneCount(trimmed) <= 12 {
		return handlerutil.RunePrefix(trimmed, 1) + "***"
	}
	return handlerutil.MaskAPIKey(trimmed)
}

// recentFromHistoryRow maps a persisted usageHistory row onto the stream shape
// so ring seeding and live pushes render identically.
func recentFromHistoryRow(rh db.UsageHistoryRow) RecentRequest {
	promptTok, complTok := translator.ResolveTokensCounts([]byte(rh.Tokens), rh.PromptTokens, rh.CompletionTokens)
	cached := translator.CachedTokensFromJSON([]byte(rh.Tokens))

	status := "ok"
	if rh.Status != "" && rh.Status != "success" && rh.Status != "ok" {
		status = rh.Status
	}

	// Account mirrors detailListItem's fallback chain as far as a history
	// row allows: the row stores no account label, so the chain lands on the
	// connection id, or "Default" for a direct (connection-less) request.
	account := rh.ConnectionID
	if account == "" {
		account = "Default"
	}

	return RecentRequest{
		Timestamp:        rh.Timestamp,
		Model:            rh.Model,
		Provider:         rh.Provider,
		PromptTokens:     int(promptTok),
		CompletionTokens: int(complTok),
		CachedTokens:     cached,
		Status:           status,
		Endpoint:         rh.Endpoint,
		APIKey:           maskRecentAPIKey(rh.APIKey),
		Account:          account,
	}
}

func parseModelKey(key string) (model, provider string) {
	// key format: "modelName (providerName)"
	if idx := strings.LastIndex(key, " ("); idx != -1 && strings.HasSuffix(key, ")") {
		return key[:idx], key[idx+2 : len(key)-1]
	}
	return key, "unknown"
}

// Subscribe registers a channel to receive encoded SSE payload events.
func (t *Tracker) Subscribe() (chan []byte, func()) {
	ch := make(chan []byte, 16)
	t.mu.Lock()
	t.subscribers[ch] = struct{}{}
	t.mu.Unlock()

	unsubscribe := func() {
		t.mu.Lock()
		delete(t.subscribers, ch)
		close(ch)
		t.mu.Unlock()
	}

	return ch, unsubscribe
}

func (t *Tracker) scheduleBroadcast(repo *db.Repo) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if repo != nil {
		t.lastRepo = repo
	}
	if t.broadcastDebounce != nil {
		t.broadcastDebounce.Stop()
	}

	t.broadcastDebounce = time.AfterFunc(50*time.Millisecond, func() {
		t.mu.Lock()
		repo := t.lastRepo
		t.mu.Unlock()

		t.ensureRingSeeded(repo)
		payload := t.buildPayload(repo)
		b, err := json.Marshal(payload)
		if err != nil {
			return
		}

		// The send stays under the subscriber lock: unsubscribe closes
		// the channel while holding the same lock, and a send racing
		// that close panics with "send on closed channel". Snapshotting
		// the channels first and sending after unlocking left exactly
		// that window open — the map was read safely, the channel itself
		// was not.
		t.mu.Lock()
		defer t.mu.Unlock()
		for ch := range t.subscribers {
			select {
			case ch <- b:
			default:
			}
		}
	})
}
