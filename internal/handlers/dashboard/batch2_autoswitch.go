package dashboard

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// Auto-switch support for the Antigravity CLI and Codex families — a port
// of AxonRouter's src/lib/antigravityAutoSwitch.js / codexAutoSwitch.js.
//
// The feature mirrors the active account into the CLI's own on-disk
// credential file (~/.gemini/antigravity-cli/antigravity-oauth-token and
// ~/.codex/auth.json) so the external CLI picks up rotations. That is a
// host-file write by design — it is what the AxonRouter contract does —
// and it is flagged in the batch report because the project's direction
// is Docker-only delivery, where these paths belong to a mounted home.

const (
	antigravityAutoSwitchKey = "antigravityAutoSwitch"
	codexAutoSwitchKey       = "codexAutoSwitch"
	antigravityProviderID    = "antigravity"
	codexProviderID          = "codex"
)

func autoSwitchSettingsKey(provider string) string {
	if provider == codexProviderID {
		return codexAutoSwitchKey
	}
	return antigravityAutoSwitchKey
}

// autoSwitchConfig reads one family's settings object from the raw
// settings ({} when unset or unreadable, like AxonRouter's catch).
func (h *DashboardHandler) autoSwitchConfig(provider string) map[string]any {
	settings, err := h.Repo.GetSettingsRaw()
	if err != nil {
		return map[string]any{}
	}
	if cfg, ok := settings[autoSwitchSettingsKey(provider)].(map[string]any); ok && cfg != nil {
		return cfg
	}
	return map[string]any{}
}

func cfgString(cfg map[string]any, key string) any {
	if v, ok := cfg[key].(string); ok && v != "" {
		return v
	}
	return nil
}

func cfgActiveID(cfg map[string]any) string {
	if v, ok := cfg["activeConnectionId"].(string); ok {
		return v
	}
	return ""
}

// normalizeCodexThreshold ports normalizeCodexThreshold: Number(value),
// non-finite → 10, clamped to a rounded 1..99.
func normalizeCodexThreshold(v any) int {
	var n float64
	switch t := v.(type) {
	case float64:
		n = t
	case int:
		n = float64(t)
	case int64:
		n = float64(t)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 10
		}
		n = parsed
	default:
		return 10
	}
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return 10
	}
	rounded := int(math.Round(n))
	if rounded < 1 {
		return 1
	}
	if rounded > 99 {
		return 99
	}
	return rounded
}

type rotationMode int

const (
	rotationKeep rotationMode = iota
	rotationClear
	rotationSet
)

// persistAutoSwitchActive ports persistActiveConnection: rewrites the
// family settings with the new active id, preserving unrelated keys, and
// applies the rotation-event bookkeeping. Errors are swallowed (the
// original only logs them).
func (h *DashboardHandler) persistAutoSwitchActive(provider string, connectionID *string, mode rotationMode, from, to *string) {
	current := h.autoSwitchConfig(provider)
	next := make(map[string]any, len(current)+4)
	for k, v := range current {
		next[k] = v
	}
	next["enabled"] = current["enabled"] == true
	if connectionID != nil {
		next["activeConnectionId"] = *connectionID
	} else {
		next["activeConnectionId"] = nil
	}
	if provider == codexProviderID {
		next["thresholdPercent"] = normalizeCodexThreshold(current["thresholdPercent"])
	}
	switch mode {
	case rotationClear:
		next["lastRotatedAt"] = nil
		next["lastRotatedFrom"] = nil
		next["lastRotatedTo"] = nil
	case rotationSet:
		next["lastRotatedAt"] = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		next["lastRotatedFrom"] = from
		next["lastRotatedTo"] = to
	case rotationKeep:
	}
	_ = h.Repo.UpdateSettingsRaw(map[string]any{autoSwitchSettingsKey(provider): next})
}
