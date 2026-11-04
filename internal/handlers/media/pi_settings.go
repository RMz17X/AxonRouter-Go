package media

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	json "encoding/json/v2"
)

// Pi settings API — read side. Port of upstream
// `src/app/api/cli-tools/pi-settings/route.ts` GET (Axon's pi route is
// TypeScript-only; the .ts is the contract).
//
// Batch 5 (P7a) retired the host-write model: the POST/DELETE apply
// and reset handlers are gone, and the dashboard composes an
// installer command instead of writing models.json on the gateway
// host.
//
// Axon reads only the agent-scoped models.json; the Go read keeps
// this gateway's historical root-file fallback (resolvePiModelsPath
// precedence) — recorded as a deviation in the batch fragment.

// PiHandler serves the pi settings API.
type PiHandler struct {
	repo *db.Repo
}

// NewPiHandler returns a PiHandler. repo is retained for wiring
// compatibility with the pre-batch-5 constructor.
func NewPiHandler(repo *db.Repo) *PiHandler { return &PiHandler{repo: repo} }

// piModelsPaths are the two locations pi reads, in upstream precedence order:
// the agent-scoped file when it exists, else the root one.
func piModelsPaths() (agent, root string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	return filepath.Join(home, ".pi", "agent", "models.json"), filepath.Join(home, ".pi", "models.json")
}

// resolvePiModelsPath picks the file a read targets: an existing agent-scoped
// file wins, then an existing root file, then the agent path pi reads first.
func resolvePiModelsPath() string {
	agent, root := piModelsPaths()
	if _, err := os.Stat(agent); err == nil {
		return agent
	}
	if _, err := os.Stat(root); err == nil {
		return root
	}
	return agent
}

// piInstalled reports whether pi is on PATH or has left a models.json behind,
// so an install without a PATH entry is still recognised.
func piInstalled() bool {
	if _, err := exec.LookPath("pi"); err == nil {
		return true
	}
	agent, root := piModelsPaths()
	if _, err := os.Stat(agent); err == nil {
		return true
	}
	_, err := os.Stat(root)
	return err == nil
}

// readPiConfig parses models.json whole. An unreadable or unparsable file
// reads as "no config", matching Axon's readPiModels behaviour
// (ENOENT and SyntaxError both yield null).
func readPiConfig() map[string]any {
	agent, root := piModelsPaths()
	for _, path := range []string{agent, root} {
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var config map[string]any
		if json.Unmarshal(raw, &config) == nil && config != nil {
			return config
		}
	}
	return nil
}

// piHas9RouterConfig backs the all-statuses card (a pre-batch-5
// surface kept unchanged): whether any provider block points at a
// local gateway — the named block with a baseUrl, or any block
// carrying one of the two default gateway ports (20128 upstream's,
// 20130 this gateway's). The settings GET itself uses Axon's
// providers["axonrouter"]-presence rule instead.
func piHas9RouterConfig(config map[string]any) bool {
	providers, _ := config["providers"].(map[string]any)
	if p, ok := providers["9router"].(map[string]any); ok {
		if b, _ := p["baseUrl"].(string); strings.TrimSpace(b) != "" {
			return true
		}
	}
	for _, v := range providers {
		p, ok := v.(map[string]any)
		if !ok {
			continue
		}
		b, _ := p["baseUrl"].(string)
		if strings.Contains(b, "20128") || strings.Contains(b, "20130") {
			return true
		}
	}
	return false
}

// HandleGet serves GET /api/cli-tools/pi-settings, in Axon's shape:
// the axonrouter provider block drives hasAxonRouter and the pi{}
// summary (model ids + baseUrl).
func (h *PiHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	if !piInstalled() {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"installed": false,
			"config":    nil,
			"message":   "Pi CLI is not installed",
		})
		return
	}
	config := readPiConfig()
	providers, _ := config["providers"].(map[string]any)
	provider, _ := providers["axonrouter"].(map[string]any)

	models := []any{}
	if list, ok := provider["models"].([]any); ok {
		for _, entry := range list {
			if m, ok := entry.(map[string]any); ok {
				if id, ok := m["id"].(string); ok {
					models = append(models, id)
					continue
				}
			}
			models = append(models, nil)
		}
	}
	var baseURL any
	if b, _ := provider["baseUrl"].(string); b != "" {
		baseURL = b
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"installed":     true,
		"config":        config,
		"hasAxonRouter": provider != nil,
		"configPath":    resolvePiModelsPath(),
		"pi": map[string]any{
			"models":  models,
			"baseURL": baseURL,
		},
	})
}
