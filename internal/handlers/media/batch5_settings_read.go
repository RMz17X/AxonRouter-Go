package media

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"axonrouter/proxy/internal/clisetup"
)

// *-settings GET readers (batch 5 re-score). Axon keeps read-only
// settings routes next to the installer: each reports whether the
// tool is installed on the machine running the dashboard and whether
// its existing config already points at the gateway. These handlers
// port those GET contracts only — the old host-write POST/DELETE
// sides are retired with the host-write model (P7a).
//
// Installed means the same as Axon's checkCliInstalled: the binary
// resolves on PATH (which/where) or a fallback config path exists.

func cliInstalled(bin string, fallbacks ...string) bool {
	if _, err := exec.LookPath(bin); err == nil {
		return true
	}
	for _, p := range fallbacks {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// localhostLike matches Axon's inline host test used by several
// settings routes: the base URL mentions localhost, 127.0.0.1,
// 0.0.0.0 or the axonrouter name.
func pointsAtGateway(baseURL string, includeZero bool) bool {
	if baseURL == "" {
		return false
	}
	if strings.Contains(baseURL, "localhost") ||
		strings.Contains(baseURL, "127.0.0.1") ||
		strings.Contains(baseURL, "axonrouter") {
		return true
	}
	return includeZero && strings.Contains(baseURL, "0.0.0.0")
}

// SettingsReadHandler serves the Axon-shape GET *-settings routes.
type SettingsReadHandler struct{}

// NewSettingsReadHandler returns a SettingsReadHandler.
func NewSettingsReadHandler() *SettingsReadHandler { return &SettingsReadHandler{} }

// --- claude ----------------------------------------------------------

func (h *SettingsReadHandler) GetClaude(w http.ResponseWriter, r *http.Request) {
	settingsPath := filepath.Join(homeDir(), ".claude", "settings.json")
	if !cliInstalled("claude", settingsPath) {
		writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
			"installed", false, "settings", nil, "message", "Claude CLI is not installed"))
		return
	}
	settings := readJSONCFile(settingsPath)
	hasAxon := truthyJS(jGet(jGet(settings, "env"), "ANTHROPIC_BASE_URL"))
	claudeJSON := readJSONCFile(filepath.Join(homeDir(), ".claude.json"))
	exa := truthyJS(jGet(jGet(claudeJSON, "mcpServers"), "exa"))
	writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
		"installed", true,
		"settings", settings,
		"hasAxonRouter", hasAxon,
		"exaMcpEnabled", exa,
		"settingsPath", settingsPath,
	))
}

// --- codex -----------------------------------------------------------

func (h *SettingsReadHandler) GetCodex(w http.ResponseWriter, r *http.Request) {
	configPath := filepath.Join(homeDir(), ".codex", "config.toml")
	if !cliInstalled("codex", configPath) {
		writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
			"installed", false, "config", nil, "message", "Codex CLI is not installed"))
		return
	}
	raw, err := os.ReadFile(configPath)
	var config any
	text := ""
	if err == nil {
		text = string(raw)
		config = text
	} else if !os.IsNotExist(err) {
		writeOrderedJSON(w, http.StatusInternalServerError, clisetup.NewJObj(
			"error", "Failed to check codex settings"))
		return
	}
	hasAxon := strings.Contains(text, `model_provider = "axonrouter"`) ||
		strings.Contains(text, "[model_providers.axonrouter]")
	writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
		"installed", true,
		"config", config,
		"hasAxonRouter", hasAxon,
		"configPath", configPath,
	))
}

// --- cline -----------------------------------------------------------

func (h *SettingsReadHandler) GetCline(w http.ResponseWriter, r *http.Request) {
	home := homeDir()
	globalStatePath := filepath.Join(home, ".cline", "data", "globalState.json")
	if !cliInstalled("cline", filepath.Join(home, ".cline")) {
		writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
			"installed", false, "settings", nil, "message", "Cline CLI is not installed"))
		return
	}
	gs := readJSONCFile(globalStatePath)
	settings := clisetup.NewJObj()
	for _, key := range []string{"actModeApiProvider", "planModeApiProvider", "openAiBaseUrl", "openAiModelId"} {
		if v := jGet(gs, key); v != nil {
			settings.Set(key, v)
		}
	}
	isOpenAI := jString(gs, "actModeApiProvider") == "openai" ||
		jString(gs, "planModeApiProvider") == "openai"
	hasAxon := isOpenAI && pointsAtGateway(jString(gs, "openAiBaseUrl"), false)
	writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
		"installed", true,
		"settings", settings,
		"hasAxonRouter", hasAxon,
		"globalStatePath", globalStatePath,
	))
}

// --- copilot ---------------------------------------------------------

func copilotConfigPath() string {
	home := homeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Code", "User", "chatLanguageModels.json")
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), "Code", "User", "chatLanguageModels.json")
	default:
		return filepath.Join(home, ".config", "Code", "User", "chatLanguageModels.json")
	}
}

// GetCopilot has no installed check in Axon — VS Code's model file is
// read whenever present and installed is always true.
func (h *SettingsReadHandler) GetCopilot(w http.ResponseWriter, r *http.Request) {
	configPath := copilotConfigPath()
	config := readJSONCFile(configPath)
	var entry any
	if arr, ok := config.([]any); ok {
		for _, e := range arr {
			if jString(e, "name") == "AxonRouter" {
				entry = e
				break
			}
		}
	}
	var currentModel, currentURL any
	if entry != nil {
		if models, ok := jGet(entry, "models").([]any); ok && len(models) > 0 {
			if id := jGet(models[0], "id"); truthyJS(id) {
				currentModel = id
			}
			if u := jGet(models[0], "url"); truthyJS(u) {
				currentURL = u
			}
		}
	}
	writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
		"installed", true,
		"config", config,
		"hasAxonRouter", entry != nil,
		"configPath", configPath,
		"currentModel", currentModel,
		"currentUrl", currentURL,
	))
}
