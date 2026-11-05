package dashboard

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"axonrouter/proxy/internal/config"
	"axonrouter/proxy/internal/handlerutil"
)

// The per-provider instructions routes (codex / commandcode / morph) —
// ports of AxonRouter's providers/{codex,commandcode,morph}/instructions
// route.ts files. Each manages a settings object ({enabled, mode}) plus
// an optional custom instructions file under the data dir; the payload
// reports the effective content (default text, custom file, or empty
// when disabled).
//
// Note: AxonRouter's executors consume these settings when building
// requests; this codebase's gateway does not yet apply provider
// instruction overrides at request time. The routes manage the settings
// and file state with the exact contract regardless.

const maxInstructionsBytes = 204800

type instructionsFamily struct {
	settingsKey string
	filename    string
	label       string
	defaultText string
	// filePathInPayload: AxonRouter's codex resolver exports a null file
	// path; the other two families report the bare filename.
	nullFilePath bool
}

var codexInstructionsFamily = instructionsFamily{
	settingsKey: "codexInstructions", filename: "codex-instructions.md",
	label: "Codex", defaultText: codexDefaultInstructions, nullFilePath: true,
}

var commandcodeInstructionsFamily = instructionsFamily{
	settingsKey: "commandcodeInstructions", filename: "commandcode-instructions.md",
	label: "Command Code", defaultText: commandcodeDefaultInstructions,
}

var morphInstructionsFamily = instructionsFamily{
	settingsKey: "morphInstructions", filename: "morph-instructions.md",
	label: "Morph", defaultText: morphDefaultInstructions,
}

// utf16Len counts UTF-16 code units — JS string length, which is what
// AxonRouter reports for the *Length fields.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func normalizeInstructionsCfg(raw any) (enabled bool, mode string) {
	enabled = true
	mode = "default"
	cfg, _ := raw.(map[string]any)
	if cfg == nil {
		return enabled, mode
	}
	if b, ok := cfg["enabled"].(bool); ok && !b {
		enabled = false
	}
	if cfg["mode"] == "custom" {
		mode = "custom"
	}
	return enabled, mode
}

func (h *DashboardHandler) instructionsCfg(fam instructionsFamily) (map[string]any, error) {
	settings, err := h.Repo.GetSettingsRaw()
	if err != nil {
		return nil, err
	}
	if cfg, ok := settings[fam.settingsKey].(map[string]any); ok && cfg != nil {
		return cfg, nil
	}
	return map[string]any{}, nil
}

func readInstructionsFile(fam instructionsFamily) string {
	raw, err := os.ReadFile(filepath.Join(config.ResolveDataDir(), fam.filename))
	if err != nil {
		return ""
	}
	return string(raw)
}

func (h *DashboardHandler) buildInstructionsPayload(fam instructionsFamily, rawCfg map[string]any) map[string]any {
	enabled, mode := normalizeInstructionsCfg(rawCfg)
	customContent := readInstructionsFile(fam)
	hasCustomFile := utf16Len(customContent) > 0
	effective := ""
	if enabled {
		if mode == "custom" && hasCustomFile {
			effective = customContent
		} else {
			effective = fam.defaultText
		}
	}
	var filePath any
	if !fam.nullFilePath {
		filePath = fam.filename
	}
	return map[string]any{
		"enabled":          enabled,
		"mode":             mode,
		"hasCustomFile":    hasCustomFile,
		"customContent":    customContent,
		"customLength":     utf16Len(customContent),
		"effectiveContent": effective,
		"effectiveLength":  utf16Len(effective),
		"defaultContent":   fam.defaultText,
		"defaultLength":    utf16Len(fam.defaultText),
		"filename":         fam.filename,
		"filePath":         filePath,
		"maxBytes":         maxInstructionsBytes,
	}
}

func (h *DashboardHandler) instructionsHandler(w http.ResponseWriter, r *http.Request, fam instructionsFamily) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := h.instructionsCfg(fam)
		if err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   fmt.Sprintf("Failed to read %s instructions settings", fam.label),
				"message": err.Error(),
			})
			return
		}
		handlerutil.WriteJSON(w, http.StatusOK, h.buildInstructionsPayload(fam, cfg))
	case http.MethodPut:
		h.updateInstructions(w, r, fam)
	default:
		handlerutil.WriteJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Method not allowed"})
	}
}

func (h *DashboardHandler) updateInstructions(w http.ResponseWriter, r *http.Request, fam instructionsFamily) {
	defer r.Body.Close()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	var parsed any
	if json.Unmarshal(raw, &parsed) != nil {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	// typeof [] === "object" in JS: an array body passes the object check
	// and simply applies no fields.
	body, isMap := parsed.(map[string]any)
	if !isMap {
		if _, isArray := parsed.([]any); !isArray {
			handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid request body"})
			return
		}
		body = map[string]any{}
	}

	cfg, err := h.instructionsCfg(fam)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"error":   fmt.Sprintf("Failed to read %s instructions settings", fam.label),
			"message": err.Error(),
		})
		return
	}
	enabled, mode := normalizeInstructionsCfg(cfg)
	if v, ok := body["enabled"].(bool); ok {
		enabled = v
	}
	if s, ok := body["mode"].(string); ok && (s == "default" || s == "custom") {
		mode = s
	}

	mutation := ""
	var writeContent string
	if body["reset"] == true {
		mutation = "delete"
		mode = "default"
	} else if content, isStr := body["content"].(string); isStr {
		if len([]byte(content)) > maxInstructionsBytes {
			handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
				"error": fmt.Sprintf("Custom instructions exceed %d bytes", maxInstructionsBytes),
			})
			return
		}
		if content == "" {
			mutation = "delete"
			mode = "default"
		} else {
			mutation = "write"
			writeContent = content
			if body["mode"] != "default" {
				mode = "custom"
			}
		}
	}

	filePath := filepath.Join(config.ResolveDataDir(), fam.filename)
	switch mutation {
	case "write":
		mutErr := os.MkdirAll(config.ResolveDataDir(), 0o755)
		if mutErr == nil {
			mutErr = os.WriteFile(filePath, []byte(writeContent), 0o644)
		}
		if mutErr != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "Failed to update custom instructions file", "message": mutErr.Error(),
			})
			return
		}
	case "delete":
		if rmErr := os.Remove(filePath); rmErr != nil && !os.IsNotExist(rmErr) {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "Failed to update custom instructions file", "message": rmErr.Error(),
			})
			return
		}
	}

	next := map[string]any{"enabled": enabled, "mode": mode}
	if err := h.Repo.UpdateSettingsRaw(map[string]any{fam.settingsKey: next}); err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "Failed to update custom instructions file", "message": err.Error(),
		})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, h.buildInstructionsPayload(fam, next))
}

func (h *DashboardHandler) HandleCodexInstructions(w http.ResponseWriter, r *http.Request) {
	h.instructionsHandler(w, r, codexInstructionsFamily)
}

func (h *DashboardHandler) HandleCommandCodeInstructions(w http.ResponseWriter, r *http.Request) {
	h.instructionsHandler(w, r, commandcodeInstructionsFamily)
}

func (h *DashboardHandler) HandleMorphInstructions(w http.ResponseWriter, r *http.Request) {
	h.instructionsHandler(w, r, morphInstructionsFamily)
}
