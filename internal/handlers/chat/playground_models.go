package chat

import (
	"context"
	"net/http"
	"strings"
	"time"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/models"
)

// playground_models.go builds the Playground's model inventory: only what a
// client request could actually be routed to right now. Combos come first
// (the same order /v1/models publishes), then one entry per (provider,
// model) pair offered by the usable connections, grouped client-side by
// provider + connection.
//
// "Usable" mirrors the serving path's own model-independent eligibility —
// the checks pinnedConnectionIneligible applies before any per-model gate:
// the connection is active, and it is not parked inside an account cooldown
// (db.ConnectionBlockedUntil — the rate-limit / OAuth-lock parks; a
// ToS-disabled account is disabled by DisableConnectionWithReason, which
// flips isActive off, so it is excluded by the first check). Per-model
// gates (model locks, strict assignment) stay the serving path's business:
// the inventory names what the connection publishes, and a gated model
// still answers with the gateway's ordinary refusal if picked.
//
// A connection's model set is computed by appendConnectionModels — the same
// per-connection builder /v1/models uses (enabledModels pin, live catalog,
// static catalog, custom models, disabled-model filtering) — run with a
// fresh seen-set per connection.
//
// The inventory is then deduped by (provider, model): a model is offered
// once per provider, attributed to the first eligible connection that
// publishes it, and at most playgroundProviderConnectionCap connections
// per provider contribute. The chat dispatch never pins a connection —
// the serving path picks the account — so per-connection duplicates of
// the same model add no capability, and at farm scale (tens of thousands
// of keys on one provider) they exploded the payload to ~314k entries /
// ~59 MB, which the picker could not load at all. The cap keeps
// per-account model pins reachable on real fleets (a provider's accounts
// number in the dozens) while bounding the farm case; 1023.

// playgroundProviderConnectionCap bounds how many connections of one
// provider contribute their model sets to the inventory (see the header
// comment). Providers with more eligible connections than the cap are
// farms of interchangeable keys publishing the provider catalog.
const playgroundProviderConnectionCap = 100

// PlaygroundModelEntry is one selectable row of the inventory. Model is the
// wire id a client would send: "provider/model" for provider models, the
// combo's name for combos.
type PlaygroundModelEntry struct {
	Type           string `json:"type"` // "model" | "combo"
	Provider       string `json:"provider"`
	ConnectionID   string `json:"connectionId,omitempty"`
	ConnectionName string `json:"connectionName,omitempty"`
	Model          string `json:"model"`
}

// HandlePlaygroundModels serves GET /api/playground/models (dashboard
// session group).
func (h *ChatHandler) HandlePlaygroundModels(w http.ResponseWriter, r *http.Request) {
	entries, err := h.playgroundInventory(r.Context())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to load playground models")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, entries)
}

func (h *ChatHandler) playgroundInventory(ctx context.Context) ([]PlaygroundModelEntry, error) {
	entries := make([]PlaygroundModelEntry, 0)
	if h.Repo == nil {
		return entries, nil
	}

	if combos, err := h.Repo.GetCombos(); err == nil {
		for _, combo := range combos {
			if combo == nil || combo.Name == "" {
				continue
			}
			entries = append(entries, PlaygroundModelEntry{
				Type:     "combo",
				Provider: "combo",
				Model:    combo.Name,
			})
		}
	}

	conns, err := h.Repo.GetProviderConnections("", true)
	if err != nil {
		return nil, err
	}

	disabled := h.disabledModelIndex()
	isDisabled := func(provider, modelID string) bool {
		return disabled[provider][modelID]
	}
	customs := h.customModelsByProvider()
	aliases := h.modelAliasTargets()
	nodePrefixes := h.providerNodePrefixMap()

	now := time.Now()
	seen := make(map[string]bool)
	contributed := make(map[string]int)
	for _, conn := range conns {
		if conn == nil || conn.Provider == "" || conn.IsActive != 1 {
			continue
		}
		// Hidden providers still serve traffic but are never offered, and
		// the playground is an offering surface — same rule as /v1/models.
		if hiddenProvider(conn.Provider) {
			continue
		}
		if until, ok := db.ConnectionBlockedUntil(conn.Data); ok && until.After(now) {
			continue
		}
		if contributed[conn.Provider] >= playgroundProviderConnectionCap {
			continue
		}
		contributed[conn.Provider]++
		name := playgroundConnectionName(conn)
		objs := h.appendConnectionModels(ctx, nil, make(map[string]bool), conn, conn.Provider,
			customs, aliases, nodePrefixes, isDisabled, nil)
		for _, obj := range objs {
			key := conn.Provider + "\x00" + obj.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			entries = append(entries, PlaygroundModelEntry{
				Type:           "model",
				Provider:       conn.Provider,
				ConnectionID:   conn.ID,
				ConnectionName: name,
				Model:          obj.ID,
			})
		}
	}
	return entries, nil
}

// playgroundConnectionName is the label the picker groups by: the
// operator-given name, else the account email, else the provider id — the
// same fallback order the connections list renders.
func playgroundConnectionName(conn *models.ProviderConnection) string {
	if conn.Name != nil && strings.TrimSpace(*conn.Name) != "" {
		return *conn.Name
	}
	if conn.Email != nil && strings.TrimSpace(*conn.Email) != "" {
		return *conn.Email
	}
	return conn.Provider
}
