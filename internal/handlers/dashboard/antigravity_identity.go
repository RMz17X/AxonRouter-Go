package dashboard

import (
	json "encoding/json/v2"

	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/models"
	"axonrouter/proxy/internal/providers"
)

// resolveAntigravityIdentity resolves the identity a dashboard surface
// presents for a connection (fragment 972): the pinned snapshot stored
// on the connection when one exists, the runtime derivation otherwise.
// Dashboard reads are a persisting context, so a connection without a
// pin gets its lazy backfill here — computed from the current runtime
// derivation, written first-writer-wins, and served from the pin from
// this call on. A failed write only defers the pin to the next touch;
// the values served are the snapshot's by construction either way.
func (h *DashboardHandler) resolveAntigravityIdentity(conn *models.ProviderConnection) providers.AntigravityIdentity {
	if conn == nil {
		return providers.AntigravityRuntimeIdentity()
	}
	var data struct {
		ProviderSpecificData map[string]any `json:"providerSpecificData"`
	}
	if conn.Data != "" {
		_ = json.Unmarshal([]byte(conn.Data), &data)
	}
	psd := data.ProviderSpecificData
	if conn.Provider == "antigravity" && h.Repo != nil && !providers.AntigravityIdentityPinned(psd) {
		snapshotJSON, err := providers.AntigravityIdentitySnapshotJSON()
		if err == nil {
			if err := h.Repo.SetConnectionAntigravityIdentity(conn.ID, snapshotJSON); err != nil {
				log.Warn("usage", "pin antigravity identity failed", "conn", conn.ID, "error", err)
			} else {
				if psd == nil {
					psd = map[string]any{}
				}
				psd[providers.AntigravityIdentityPSDKey] = providers.AntigravityIdentitySnapshot()
			}
		}
	}
	return providers.AntigravityIdentityForPSD(psd)
}
