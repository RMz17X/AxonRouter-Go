package media

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"9router/proxy/internal/clisetup"
)

// Cowork (Claude Desktop 3p) settings reader — the read half of
// Axon's cowork-settings route. The gateway no longer writes the
// desktop app's managed config; the dashboard reads what is applied
// and composes an installer command instead (P7a).

// coworkDefaultPlugins / coworkLocalStdioPlugins port
// src/lib/cliTools/coworkPlugins.js verbatim.
func coworkDefaultPlugins() []any {
	return []any{
		clisetup.NewJObj(
			"name", "exa",
			"title", "Exa",
			"description", "Real-time web search and code documentation",
			"url", "https://mcp.exa.ai/mcp",
			"transport", "http",
			"oauth", false,
			"toolNames", []any{"web_search_exa", "web_fetch_exa"},
		),
		clisetup.NewJObj(
			"name", "tavily",
			"title", "Tavily",
			"description", "Real-time web search optimized for LLM agents",
			"url", "https://mcp.tavily.com/mcp",
			"transport", "http",
			"oauth", true,
			"toolNames", []any{"tavily_search", "tavily_extract", "tavily_crawl", "tavily_map"},
		),
	}
}

func coworkLocalStdioPlugins() []any {
	return []any{
		clisetup.NewJObj(
			"name", "browsermcp",
			"title", "Browser MCP",
			"description", "Control your running Chrome (requires Chrome extension)",
			"extensionUrl", "https://chromewebstore.google.com/detail/browser-mcp-automate-your/bjfgambnhccakkhmkepdoekmckoijdlc",
			"command", "npx",
			"args", []any{"-y", "@browsermcp/mcp@latest"},
			"toolNames", []any{
				"browser_navigate", "browser_snapshot", "browser_click", "browser_type",
				"browser_screenshot", "browser_get_console_logs", "browser_wait",
				"browser_press_key", "browser_go_back", "browser_go_forward",
			},
		),
	}
}

// coworkCandidateRoots mirrors Axon's per-platform Claude Desktop
// config roots, most-preferred first.
func coworkCandidateRoots() []string {
	home := homeDir()
	switch runtime.GOOS {
	case "darwin":
		base := filepath.Join(home, "Library", "Application Support")
		return []string{filepath.Join(base, "Claude-3p"), filepath.Join(base, "Claude")}
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		roaming := os.Getenv("APPDATA")
		return []string{
			filepath.Join(local, "Claude-3p"), filepath.Join(local, "Claude"),
			filepath.Join(roaming, "Claude-3p"), filepath.Join(roaming, "Claude"),
		}
	default:
		cfg := filepath.Join(home, ".config")
		return []string{filepath.Join(cfg, "Claude-3p"), filepath.Join(cfg, "Claude")}
	}
}

func coworkAppInstallPaths() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/Applications/Claude.app"}
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		return []string{
			filepath.Join(local, "Programs", "Claude"),
			filepath.Join(local, "AnthropicClaude"),
		}
	default:
		return nil
	}
}

// GetCowork serves GET /api/cli-tools/cowork-settings.
func (h *SettingsReadHandler) GetCowork(w http.ResponseWriter, r *http.Request) {
	roots := coworkCandidateRoots()
	installed := false
	for _, p := range append(append([]string{}, roots...), coworkAppInstallPaths()...) {
		if _, err := os.Stat(p); err == nil {
			installed = true
			break
		}
	}
	if !installed {
		writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
			"installed", false, "config", nil,
			"message", "Claude Desktop (Cowork mode) not detected"))
		return
	}

	// Read root: first root that already holds a configLibrary,
	// else the preferred root.
	readRoot := roots[0]
	for _, root := range roots {
		if st, err := os.Stat(filepath.Join(root, "configLibrary")); err == nil && st.IsDir() {
			readRoot = root
			break
		}
	}
	libraryDir := filepath.Join(readRoot, "configLibrary")
	meta := readJSONCFile(filepath.Join(libraryDir, "_meta.json"))
	var appliedID any
	if id := jGet(meta, "appliedId"); truthyJS(id) {
		appliedID = id
	}
	var config, configPath any
	if idStr, ok := appliedID.(string); ok && idStr != "" {
		p := filepath.Join(libraryDir, idStr+".json")
		configPath = p
		config = readJSONCFile(p)
	}

	hasAxon := jString(config, "inferenceProvider") == "gateway" &&
		truthyJS(jGet(config, "inferenceGatewayBaseUrl"))

	writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
		"installed", true,
		"config", config,
		"hasAxonRouter", hasAxon,
		"configPath", configPath,
		"cowork", buildCoworkSummary(config, appliedID),
		"defaultPlugins", coworkDefaultPlugins(),
		"localStdioPlugins", coworkLocalStdioPlugins(),
	))
}

// buildCoworkSummary computes the cowork{} block: applied identity
// plus the managed-MCP classification (default plugins / local stdio
// bridges / custom plugins).
func buildCoworkSummary(config any, appliedID any) *clisetup.JObj {
	stdioNames := map[string]bool{"browsermcp": true}
	isBridge := func(m any) bool {
		url, ok := jGet(m, "url").(string)
		return stdioNames[jString(m, "name")] && ok && strings.Contains(url, "/api/mcp/")
	}

	managed, _ := jGet(config, "managedMcpServers").([]any)

	localPlugins := []any{}
	for _, m := range managed {
		if isBridge(m) {
			localPlugins = append(localPlugins, jString(m, "name"))
		}
	}
	customPlugins := []any{}
	for _, m := range managed {
		name := jString(m, "name")
		url, _ := jGet(m, "url").(string)
		if truthyJS(jGet(m, "custom")) || (!stdioNames[name] && strings.Contains(url, "/api/mcp/")) {
			customPlugins = append(customPlugins, clisetup.NewJObj(
				"name", jGet(m, "name"),
				"url", jGet(m, "url"),
				"transport", jGet(m, "transport"),
				"custom", true,
			))
		}
	}
	plugins := []any{}
	for _, m := range managed {
		if truthyJS(jGet(m, "custom")) || isBridge(m) {
			continue
		}
		name := jString(m, "name")
		plugins = append(plugins, clisetup.NewJObj(
			"name", jGet(m, "name"),
			"url", jGet(m, "url"),
			"transport", jGet(m, "transport"),
			"oauth", truthyJS(jGet(m, "oauth")),
			"toolNames", coworkPluginToolNames(m, name),
		))
	}

	models := []any{}
	if list, ok := jGet(config, "inferenceModels").([]any); ok {
		for _, entry := range list {
			switch t := entry.(type) {
			case string:
				if t != "" {
					models = append(models, t)
				}
			default:
				if name := jString(entry, "name"); name != "" {
					models = append(models, name)
				}
			}
		}
	}
	var baseURL, provider any
	if b := jGet(config, "inferenceGatewayBaseUrl"); truthyJS(b) {
		baseURL = b
	}
	if p := jGet(config, "inferenceProvider"); truthyJS(p) {
		provider = p
	}
	return clisetup.NewJObj(
		"appliedId", appliedID,
		"baseUrl", baseURL,
		"models", models,
		"provider", provider,
		"plugins", plugins,
		"localPlugins", localPlugins,
		"customPlugins", customPlugins,
	)
}

// coworkPluginToolNames resolves a managed server's tool names: the
// curated list when the server is one of the default plugins, else
// the bare names recovered from its toolPolicy keys (repeated
// "<name>-" prefixes stripped, deduped, insertion order).
func coworkPluginToolNames(m any, name string) []any {
	for _, p := range coworkDefaultPlugins() {
		if jString(p, "name") == name {
			if names, ok := jGet(p, "toolNames").([]any); ok {
				return names
			}
		}
	}
	out := []any{}
	seen := map[string]bool{}
	prefix := name + "-"
	for _, key := range jKeys(jGet(m, "toolPolicy")) {
		bare := key
		for strings.HasPrefix(bare, prefix) {
			bare = bare[len(prefix):]
		}
		if !seen[bare] {
			seen[bare] = true
			out = append(out, bare)
		}
	}
	return out
}
