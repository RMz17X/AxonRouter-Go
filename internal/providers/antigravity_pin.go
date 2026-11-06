package providers

import (
	json "encoding/json/v2"
)

// antigravity_pin.go is the per-account half of the Antigravity identity
// (fragment 972). The identity's SHAPE stays the agy CLI shape defined in
// antigravity_identity.go; what changes is where a surface gets it from.
// The JS router this codebase mirrors holds one rule — one account = one
// fixed identity for the account's whole life — while the Go port's
// suspensions came from identity drift. A runtime-derived identity is
// stable only until the deployment moves OS or architecture, so each
// connection now pins a snapshot of the derivation once (at OAuth
// creation, or lazily on first use) and every surface serves the pin
// thereafter.
//
// The snapshot is ONE JSON value under one providerSpecificData key:
//
//	"antigravityIdentity": {
//	    "userAgent":  "antigravity/cli/1.5.1 (aidev_client; …)",
//	    "platform":   3,
//	    "ideVersion": "1.5.1"
//	}
//
// userAgent alone would imply the other two on today's derivation, but
// the snapshot stores all three runtime-varying parts explicitly: the
// pin must keep meaning the same thing after the mirrored CLI version
// or the derivation rules move on. The legacy clientProfile PSD value is
// NOT reused — it holds the retired ide/cli enum, not an identity.

// AntigravityIdentityPSDKey is the providerSpecificData key the pinned
// identity snapshot lives under.
const AntigravityIdentityPSDKey = "antigravityIdentity"

// AntigravityIdentity is one resolved Antigravity client identity: the
// full User-Agent, the loadCodeAssist platform enum, and the ideVersion
// the metadata reports. Surfaces consume this instead of calling the
// runtime derivation functions individually.
type AntigravityIdentity struct {
	UserAgent  string
	Platform   int
	IDEVersion string
}

// LoadCodeAssistMetadata returns the ClientMetadata body for this
// identity, in the same numeric shape AntigravityLoadCodeAssistMetadata
// documents (the fields the teardown could not establish stay omitted).
func (id AntigravityIdentity) LoadCodeAssistMetadata() map[string]any {
	return map[string]any{
		"ideType":    9, // ANTIGRAVITY
		"ideVersion": id.IDEVersion,
		"platform":   id.Platform,
		"pluginType": 2, // GEMINI
		"ideName":    "antigravity",
	}
}

// AntigravityRuntimeIdentity derives the identity from this process's
// runtime — the default for surfaces with no connection in scope, and
// the value a fresh pin snapshots.
func AntigravityRuntimeIdentity() AntigravityIdentity {
	return AntigravityIdentity{
		UserAgent:  AntigravityUserAgent(),
		Platform:   AntigravityPlatform(),
		IDEVersion: AntigravityCLIVersion,
	}
}

// AntigravityIdentitySnapshot returns the PSD value a fresh pin stores:
// the current runtime derivation in the snapshot's stored shape.
func AntigravityIdentitySnapshot() map[string]any {
	id := AntigravityRuntimeIdentity()
	return map[string]any{
		"userAgent":  id.UserAgent,
		"platform":   id.Platform,
		"ideVersion": id.IDEVersion,
	}
}

// AntigravityIdentitySnapshotJSON is the snapshot as JSON text, for the
// DB writers that persist it (Repo.SetConnectionAntigravityIdentity and
// the OAuth exchange).
func AntigravityIdentitySnapshotJSON() (string, error) {
	b, err := json.Marshal(AntigravityIdentitySnapshot())
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// pinnedAntigravityIdentity extracts a valid pin from a PSD map. A pin
// is valid only when all three parts are present and well-formed; a
// malformed value (hand-edited data — only this codebase writes the
// key, always in snapshot shape) reads as unpinned.
func pinnedAntigravityIdentity(psd map[string]any) (AntigravityIdentity, bool) {
	if psd == nil {
		return AntigravityIdentity{}, false
	}
	raw, ok := psd[AntigravityIdentityPSDKey].(map[string]any)
	if !ok {
		return AntigravityIdentity{}, false
	}
	ua, _ := raw["userAgent"].(string)
	ideVersion, _ := raw["ideVersion"].(string)
	platform, isNum := raw["platform"].(float64)
	if ua == "" || ideVersion == "" || !isNum {
		return AntigravityIdentity{}, false
	}
	return AntigravityIdentity{
		UserAgent:  ua,
		Platform:   int(platform),
		IDEVersion: ideVersion,
	}, true
}

// AntigravityIdentityPinned reports whether the PSD carries a valid
// pin. Backfill sites use it to decide whether a first-touch write is
// due; the DB write itself is additionally guarded first-writer-wins.
func AntigravityIdentityPinned(psd map[string]any) bool {
	_, ok := pinnedAntigravityIdentity(psd)
	return ok
}

// AntigravityIdentityForPSD resolves the identity a connection presents:
// its pinned snapshot when one is stored, the runtime derivation
// otherwise. A nil PSD (no connection in scope) resolves to the runtime
// derivation.
func AntigravityIdentityForPSD(psd map[string]any) AntigravityIdentity {
	if id, ok := pinnedAntigravityIdentity(psd); ok {
		return id
	}
	return AntigravityRuntimeIdentity()
}
