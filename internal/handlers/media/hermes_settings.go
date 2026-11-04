package media

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"9router/proxy/internal/handlerutil"
)

// Hermes per-profile settings API — read side. Port of upstream
// `src/app/api/cli-tools/hermes-settings/route.js` GET.
//
// Batch 5 (P7a) retired the host-write model: the POST/DELETE apply
// and reset handlers are gone, and the dashboard composes an
// installer command instead of writing config.yaml on the gateway
// host. The YAML write builders in hermes_yaml.go remain as library
// code; no HTTP route reaches them.
//
// Every response carries the profile it describes. `?profile=` is
// accepted because the CLI quick-setup path reaches for the query
// string.

// HermesHandler serves the Hermes profile list and the profile-scoped
// settings API. Stateless: every value comes off disk per request.
type HermesHandler struct{}

// NewHermesHandler returns a HermesHandler.
func NewHermesHandler() *HermesHandler { return &HermesHandler{} }

// hermesSettings is the parsed config of one profile home.
type hermesSettings struct {
	Model      *hermesModelBlock        `json:"model"`
	Delegation *hermesDelegationBlock   `json:"delegation"`
	Auxiliary  map[string]hermesAuxRole `json:"auxiliary"`
}

// HandleProfiles serves GET /api/cli-tools/hermes-profiles.
func (h *HermesHandler) HandleProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := listHermesProfiles()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to list hermes profiles")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"profiles": profiles})
}

// HandleGet serves GET /api/cli-tools/hermes-settings[?profile=].
func (h *HermesHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	// The profile is validated first: a bad name is a caller error whatever
	// the install state, and answering "not installed" would hide it.
	home, ok := resolveHermesHomeParam(w, r.URL.Query().Get("profile"))
	if !ok {
		return
	}
	if !hermesInstalled(home) {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"installed": false,
			"settings":  nil,
			"message":   "Hermes Agent is not installed",
		})
		return
	}
	yaml, err := readHermesText(home.ConfigPath)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to check hermes settings")
		return
	}
	settings := parseHermesSettings(yaml)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"installed":     true,
		"profile":       home,
		"settings":      settings,
		"hasAxonRouter": settings.has9RouterConfig(),
		"configPath":    home.ConfigPath,
	})
}

// parseHermesSettings reads every block of one config.yaml.
func parseHermesSettings(yaml string) hermesSettings {
	return hermesSettings{
		Model:      parseHermesModelBlock(yaml),
		Delegation: parseHermesDelegationBlock(yaml),
		Auxiliary:  parseHermesAuxRoles(yaml),
	}
}

// has9RouterConfig reports whether any block points at a local gateway.
func (s hermesSettings) has9RouterConfig() bool {
	if has9RouterConfig(s.Model.provider(), s.Model.baseURL()) || has9RouterConfig(s.Delegation.provider(), s.Delegation.baseURL()) {
		return true
	}
	for _, cfg := range s.Auxiliary {
		if has9RouterConfig(cfg.Provider, cfg.BaseURL) {
			return true
		}
	}
	return false
}

// resolveHermesHomeParam resolves the target profile and answers 400/404
// itself when it cannot be used. A false return means the response is written.
func resolveHermesHomeParam(w http.ResponseWriter, profile string) (hermesHome, bool) {
	home, err := resolveHermesHome(profile)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return hermesHome{}, false
	}
	// Named profile homes are created by `hermes profile create`, never by us.
	// A missing one is 404, so the dashboard can fall back to default.
	if !home.IsDefault && !dirExists(home.Dir) {
		handlerutil.WriteJSONError(w, http.StatusNotFound, fmt.Sprintf("Hermes profile %q not found", home.Name))
		return hermesHome{}, false
	}
	return home, true
}

// hermesInstalled reports whether Hermes is on PATH, falling back to the
// presence of the profile's own config so an install without a PATH entry (a
// GUI launch, a service) is still recognised.
func hermesInstalled(home hermesHome) bool {
	if _, err := exec.LookPath("hermes"); err == nil {
		return true
	}
	_, err := os.Stat(filepath.Clean(home.ConfigPath))
	return err == nil
}
