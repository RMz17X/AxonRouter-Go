package handlers

// Batch 6 — GET /api/translator/load and POST /api/translator/save,
// ported from AxonRouter src/app/api/translator/{load,save}/route.ts.
// The translator lab persists its eight pipeline stage files under
// logs/translator; both routes whitelist the same fixed filenames and
// answer the {success, ...} envelope. Axon roots the directory at
// process.cwd()/logs/translator; the port roots it at the gateway's
// data dir (config.ResolveDataDir()), the Go/Docker state-volume
// equivalent — load and save stay consistent with each other, which is
// the observable contract of the pair (noted in .changes/917).

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"axonrouter/proxy/internal/config"
	"axonrouter/proxy/internal/handlerutil"
)

var translatorStageFileWhitelist = map[string]bool{
	"1_req_client.json":  true,
	"2_req_source.json":  true,
	"3_req_openai.json":  true,
	"4_req_target.json":  true,
	"5_res_provider.txt": true,
	"6_res_openai.txt":   true,
	"7_res_client.txt":   true,
	"7_res_client.json":  true,
}

func translatorLogsDir() string {
	return filepath.Join(config.ResolveDataDir(), "logs", "translator")
}

func translatorFail(w http.ResponseWriter, status int, message string) {
	handlerutil.WriteJSON(w, status, map[string]any{
		"success": false,
		"error":   message,
	})
}

// HandleTranslatorLoad serves GET /api/translator/load?file=<name>.
func HandleTranslatorLoad(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("file")
	if file == "" {
		translatorFail(w, http.StatusBadRequest, "File parameter required")
		return
	}
	if !translatorStageFileWhitelist[file] {
		translatorFail(w, http.StatusBadRequest, "Invalid file name")
		return
	}
	content, err := os.ReadFile(filepath.Join(translatorLogsDir(), file))
	if err != nil {
		if os.IsNotExist(err) {
			translatorFail(w, http.StatusNotFound, "File not found")
			return
		}
		translatorFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"content": string(content),
	})
}

// HandleTranslatorSave serves POST /api/translator/save {file, content}.
func HandleTranslatorSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		File    string  `json:"file"`
		Content *string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		translatorFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.File == "" || body.Content == nil {
		translatorFail(w, http.StatusBadRequest, "File and content required")
		return
	}
	if !translatorStageFileWhitelist[body.File] {
		translatorFail(w, http.StatusBadRequest, "Invalid file name")
		return
	}
	dir := translatorLogsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		translatorFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(filepath.Join(dir, body.File), []byte(*body.Content), 0o644); err != nil {
		translatorFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}
