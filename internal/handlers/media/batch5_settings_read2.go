package media

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"axonrouter/proxy/internal/clisetup"
)

// Second half of the *-settings readers: deepseek-tui, droid,
// grok-build, jcode, kilo, openclaw, opencode. See
// batch5_settings_read.go for the shared contract.

// --- deepseek-tui ----------------------------------------------------

// parseDeepseekToml ports Axon's intentionally naive line parser:
// [section] headers become flat keys holding an object of the
// section's string key-values; quoted values are unquoted.
func parseDeepseekToml(content string) *clisetup.JObj {
	result := clisetup.NewJObj()
	current := result
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := strings.TrimSpace(line[1 : len(line)-1])
			current = clisetup.NewJObj()
			result.Set(name, current)
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 && strings.HasPrefix(val, `"`) && strings.HasSuffix(val, `"`) {
			val = val[1 : len(val)-1]
		}
		current.Set(key, val)
	}
	return result
}

var loopbackRE = regexp.MustCompile(`localhost|127\.0\.0\.1|0\.0\.0\.0`)

func (h *SettingsReadHandler) GetDeepseekTui(w http.ResponseWriter, r *http.Request) {
	configPath := filepath.Join(homeDir(), ".deepseek", "config.toml")
	if !cliInstalled("deepseek", configPath) {
		writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
			"installed", false, "settings", nil, "message", "DeepSeek TUI is not installed"))
		return
	}
	raw, err := os.ReadFile(configPath)
	content := ""
	if err == nil {
		content = string(raw)
	} else if !os.IsNotExist(err) {
		writeOrderedJSON(w, http.StatusInternalServerError, clisetup.NewJObj(
			"error", "Failed to check deepseek-tui settings"))
		return
	}
	settings := parseDeepseekToml(content)
	baseURL := jString(jGet(settings, "providers.openai"), "base_url")
	hasAxon := jString(settings, "provider") == "openai" && loopbackRE.MatchString(baseURL)
	writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
		"installed", true,
		"settings", settings,
		"hasAxonRouter", hasAxon,
		"configPath", configPath,
	))
}

// --- droid -----------------------------------------------------------

func (h *SettingsReadHandler) GetDroid(w http.ResponseWriter, r *http.Request) {
	settingsPath := filepath.Join(homeDir(), ".factory", "settings.json")
	if !cliInstalled("droid", settingsPath) {
		writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
			"installed", false, "settings", nil, "message", "Factory Droid CLI is not installed"))
		return
	}
	settings := readJSONCFile(settingsPath)
	hasAxon := false
	if models, ok := jGet(settings, "customModels").([]any); ok {
		for _, m := range models {
			if strings.HasPrefix(jString(m, "id"), "custom:AxonRouter") {
				hasAxon = true
				break
			}
		}
	}
	writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
		"installed", true,
		"settings", settings,
		"hasAxonRouter", hasAxon,
		"settingsPath", settingsPath,
	))
}

// --- grok-build ------------------------------------------------------

// grokSectionBody returns the raw body lines under a [section]
// header (everything up to the next line starting with "["), the way
// Axon's section regex captures them.
func grokSectionBody(content, section string) (string, bool) {
	header := "[" + section + "]"
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.TrimRight(line, " \t\r") != header {
			continue
		}
		var body []string
		for _, l := range lines[i+1:] {
			if strings.HasPrefix(l, "[") {
				break
			}
			body = append(body, l)
		}
		text := strings.Join(body, "\n")
		if len(body) > 0 {
			text += "\n"
		}
		return text, true
	}
	return "", false
}

var (
	grokStringFieldRE = regexp.MustCompile(`^[ \t]*([A-Za-z0-9_.-]+)[ \t]*=[ \t]*"([^"]*)"`)
	grokNumberFieldRE = regexp.MustCompile(`^[ \t]*([A-Za-z0-9_.-]+)[ \t]*=[ \t]*([0-9]+(?:\.[0-9]+)?)`)
)

func grokSectionString(content, section, key string) (string, bool) {
	body, found := grokSectionBody(content, section)
	if !found {
		return "", false
	}
	for _, line := range strings.Split(body, "\n") {
		if m := grokStringFieldRE.FindStringSubmatch(line); m != nil && m[1] == key {
			return m[2], true
		}
	}
	return "", false
}

func grokSectionNumber(content, section, key string) (float64, bool) {
	body, found := grokSectionBody(content, section)
	if !found {
		return 0, false
	}
	for _, line := range strings.Split(body, "\n") {
		if m := grokNumberFieldRE.FindStringSubmatch(line); m != nil && m[1] == key {
			n, err := strconv.ParseFloat(m[2], 64)
			return n, err == nil
		}
	}
	return 0, false
}

// parseGrokModelSection ports parseModelSection: the parsed
// [model.<slot>] table including its raw body text.
func parseGrokModelSection(content, slot string) *clisetup.JObj {
	body, found := grokSectionBody(content, "model."+slot)
	if !found {
		return nil
	}
	str := func(key string) any {
		if v, ok := grokSectionString(content, "model."+slot, key); ok {
			return v
		}
		return nil
	}
	var ctxWindow any
	if n, ok := grokSectionNumber(content, "model."+slot, "context_window"); ok && n > 0 {
		ctxWindow = n
	}
	return clisetup.NewJObj(
		"model", str("model"),
		"base_url", str("base_url"),
		"name", str("name"),
		"api_key", str("api_key"),
		"api_backend", str("api_backend"),
		"context_window", ctxWindow,
		"raw", body,
	)
}

// parseGrokBuildConfig ports grokBuildConfig.js's parse function.
func parseGrokBuildConfig(content string) *clisetup.JObj {
	out := clisetup.NewJObj()
	out.Set("model", parseGrokModelSection(content, "axonrouter"))
	if v, ok := grokSectionString(content, "models", "default"); ok {
		out.Set("default", v)
	}
	subagentModels := clisetup.NewJObj()
	subagentMappings := clisetup.NewJObj()
	for _, agentType := range []string{"general-purpose", "explore", "plan"} {
		mapping, _ := grokSectionString(content, "subagents.models", agentType)
		var mappingVal any
		if mapping != "" {
			mappingVal = mapping
		}
		subagentMappings.Set(agentType, mappingVal)
		var parsed *clisetup.JObj
		if mapping == "axonrouter-"+agentType {
			parsed = parseGrokModelSection(content, mapping)
		}
		subagentModels.Set(agentType, parsed)
	}
	out.Set("subagentModels", subagentModels)
	out.Set("subagentMappings", subagentMappings)
	return out
}

func (h *SettingsReadHandler) GetGrokBuild(w http.ResponseWriter, r *http.Request) {
	home := homeDir()
	configPath := filepath.Join(home, ".grok", "config.toml")
	installed := cliInstalled("grok", filepath.Join(home, ".grok", "bin", "grok"), configPath)
	if !installed {
		writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
			"installed", false, "settings", nil, "message", "Grok Build is not installed"))
		return
	}
	raw, err := os.ReadFile(configPath)
	content := ""
	if err == nil {
		content = string(raw)
	} else if !os.IsNotExist(err) {
		writeOrderedJSON(w, http.StatusInternalServerError, clisetup.NewJObj(
			"error", "Failed to check grok-build settings"))
		return
	}
	settings := parseGrokBuildConfig(content)
	hasAxon := truthyJS(jGet(jGet(settings, "model"), "base_url"))
	writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
		"installed", true,
		"settings", settings,
		"hasAxonRouter", hasAxon,
		"configPath", configPath,
	))
}

// --- jcode -----------------------------------------------------------

func (h *SettingsReadHandler) GetJcode(w http.ResponseWriter, r *http.Request) {
	home := homeDir()
	configPath := filepath.Join(home, ".jcode", "config.toml")
	if !cliInstalled("jcode", filepath.Join(home, ".jcode")) {
		writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
			"installed", false,
			"message", "jcode not installed. Install via: curl -fsSL https://raw.githubusercontent.com/1jehuang/jcode/master/scripts/install.sh | bash"))
		return
	}
	config := map[string]any{"providers": map[string]any{}}
	if raw, err := os.ReadFile(configPath); err == nil {
		parsed := map[string]any{}
		if uerr := toml.Unmarshal(raw, &parsed); uerr == nil {
			config = parsed
		}
	}
	providers, _ := config["providers"].(map[string]any)
	hasAxon := false
	if _, ok := providers["axonrouter"]; ok {
		hasAxon = true
	} else {
		for _, p := range providers {
			if pm, ok := p.(map[string]any); ok {
				if base, ok := pm["base_url"].(string); ok &&
					(strings.Contains(base, "3777") || strings.Contains(base, "3778")) {
					hasAxon = true
					break
				}
			}
		}
	}
	writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
		"installed", true,
		"config", clisetup.FromMap(config),
		"hasAxonRouter", hasAxon,
		"configPath", configPath,
	))
}

// --- kilo ------------------------------------------------------------

func (h *SettingsReadHandler) GetKilo(w http.ResponseWriter, r *http.Request) {
	home := homeDir()
	authPath := filepath.Join(home, ".local", "share", "kilo", "auth.json")
	if !cliInstalled("kilo", filepath.Join(home, ".local", "share", "kilo")) {
		writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
			"installed", false, "settings", nil, "message", "Kilo Code CLI is not installed"))
		return
	}
	auth := readJSONCFile(authPath)
	keyNames := []any{}
	for _, k := range jKeys(auth) {
		keyNames = append(keyNames, k)
	}
	check := func(provider any) bool {
		base := jString(provider, "baseUrl")
		if base == "" {
			base = jString(provider, "baseURL")
		}
		return pointsAtGateway(base, false)
	}
	hasAxon := check(jGet(auth, "openai-compatible")) || check(jGet(auth, "axonrouter"))
	writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
		"installed", true,
		"settings", clisetup.NewJObj("auth", keyNames),
		"hasAxonRouter", hasAxon,
		"authPath", authPath,
	))
}

// --- openclaw --------------------------------------------------------

func resolveOpenclawAgentModel(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case *clisetup.JObj:
		if s, ok := jGet(t, "primary").(string); ok {
			return s
		}
		return ""
	default:
		return ""
	}
}

// readOpenclawAgentCurrentModel reads <agentDir>/models.json and
// returns the first axonrouter model id (prefix stripped), or nil.
func readOpenclawAgentCurrentModel(agentDir string) any {
	if agentDir == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(agentDir, "models.json"))
	if err != nil {
		return nil
	}
	cfg, err := decodeOrderedJSON(raw)
	if err != nil {
		return nil
	}
	models := jGet(jGet(jGet(cfg, "providers"), "axonrouter"), "models")
	if arr, ok := models.([]any); ok && len(arr) > 0 {
		if id := jString(arr[0], "id"); id != "" {
			return strings.TrimPrefix(id, "axonrouter/")
		}
	}
	return nil
}

func (h *SettingsReadHandler) GetOpenclaw(w http.ResponseWriter, r *http.Request) {
	settingsPath := filepath.Join(homeDir(), ".openclaw", "openclaw.json")
	if !cliInstalled("openclaw", settingsPath) {
		writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
			"installed", false, "settings", nil, "message", "Open Claw CLI is not installed"))
		return
	}
	settings := readJSONCFile(settingsPath)
	agents := []any{}
	if list, ok := jGet(jGet(settings, "agents"), "list").([]any); ok {
		for _, a := range list {
			ao, ok := a.(*clisetup.JObj)
			if !ok {
				continue
			}
			clone := clisetup.NewJObj()
			for _, k := range ao.Keys() {
				clone.Set(k, ao.Get(k))
			}
			clone.Set("model", resolveOpenclawAgentModel(ao.Get("model")))
			clone.Set("currentModel", readOpenclawAgentCurrentModel(jString(ao, "agentDir")))
			agents = append(agents, clone)
		}
	}
	hasAxon := jGet(jGet(jGet(settings, "models"), "providers"), "axonrouter") != nil
	writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
		"installed", true,
		"settings", settings,
		"agents", agents,
		"hasAxonRouter", hasAxon,
		"settingsPath", settingsPath,
	))
}

// --- opencode --------------------------------------------------------

func (h *SettingsReadHandler) GetOpencode(w http.ResponseWriter, r *http.Request) {
	configPath := filepath.Join(homeDir(), ".config", "opencode", "opencode.json")
	if !cliInstalled("opencode", configPath) {
		writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
			"installed", false, "config", nil, "message", "OpenCode CLI is not installed"))
		return
	}
	config := readJSONCFile(configPath)
	provider := jGet(jGet(config, "provider"), "axonrouter")
	modelNames := []any{}
	for _, k := range jKeys(jGet(provider, "models")) {
		modelNames = append(modelNames, k)
	}
	var activeModel any
	if m, ok := jGet(config, "model").(string); ok && strings.HasPrefix(m, "axonrouter/") {
		activeModel = strings.TrimPrefix(m, "axonrouter/")
	}
	var baseURL any
	if b := jGet(jGet(provider, "options"), "baseURL"); truthyJS(b) {
		baseURL = b
	}
	writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
		"installed", true,
		"config", config,
		"hasAxonRouter", provider != nil,
		"configPath", configPath,
		"opencode", clisetup.NewJObj(
			"models", modelNames,
			"activeModel", activeModel,
			"baseURL", baseURL,
		),
	))
}
