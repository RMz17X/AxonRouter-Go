package media

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/clisetup"
)

// SetupHandler serves GET /api/cli-tools/setup/{toolId} — Axon's
// installer endpoint (P7a). It is public by design (Axon lists
// /api/cli-tools/setup in the dashboard guard's public paths): the
// dashboard composes the URL with the user's own client API key and
// the user pastes the curl/irm command into a terminal on their own
// machine, where no dashboard session exists. The script content is
// derived only from the query params.
type SetupHandler struct{}

// NewSetupHandler returns a SetupHandler.
func NewSetupHandler() *SetupHandler { return &SetupHandler{} }

// HandleSetup ports src/app/api/cli-tools/setup/[toolId]/route.js:
// query params baseUrl (inferred from Host when absent), apiKey,
// model, subagentModel, maxContextTokens, the sonnet/opus/haiku/fable
// tier mappings, and a comma-separated modelsList; format=ps1 selects
// PowerShell, anything else bash. Unknown tool → plain-text 404;
// script responses are no-store.
func (h *SetupHandler) HandleSetup(w http.ResponseWriter, r *http.Request) {
	toolID := chi.URLParam(r, "toolId")
	if _, ok := clisetup.Lookup(toolID); !ok {
		writeSetupText(w, http.StatusNotFound, "Error: Tool '"+toolID+"' not supported for automated setup.\n")
		return
	}

	q := r.URL.Query()
	format := "bash"
	if q.Get("format") == "ps1" {
		format = "ps1"
	}

	hostHeader := r.Host
	if hostHeader == "" {
		hostHeader = "localhost:3777"
	}
	protocol := r.Header.Get("X-Forwarded-Proto")
	if protocol == "" {
		if strings.Contains(hostHeader, "localhost") {
			protocol = "http"
		} else {
			protocol = "https"
		}
	}
	baseURL := q.Get("baseUrl")
	if baseURL == "" {
		baseURL = protocol + "://" + hostHeader
	}

	p := clisetup.Params{
		BaseURL: baseURL,
		APIKey:  q.Get("apiKey"),
		Models:  map[string]string{},
	}
	if v := q.Get("model"); v != "" {
		p.Model, p.HasModel = v, true
		p.Models["model"] = v
	}
	if v := q.Get("subagentModel"); v != "" {
		p.SubagentModel, p.HasSubagentModel = v, true
	}
	if v := q.Get("maxContextTokens"); v != "" {
		p.MaxContextTokens, p.HasMaxContext = v, true
	}
	for _, tier := range []string{"sonnet", "opus", "haiku", "fable"} {
		if v := q.Get(tier); v != "" {
			p.Models[tier] = v
		}
	}
	if _, present := q["modelsList"]; present && q.Get("modelsList") != "" {
		p.HasModelsList = true
		for _, s := range strings.Split(q.Get("modelsList"), ",") {
			if s = strings.TrimSpace(s); s != "" {
				p.ModelsList = append(p.ModelsList, s)
			}
		}
	}

	var script, contentType, filename string
	if format == "ps1" {
		script = clisetup.GeneratePowerShellScript(toolID, p)
		contentType = "text/plain; charset=utf-8"
		filename = "setup-" + toolID + ".ps1"
	} else {
		script = clisetup.GenerateBashScript(toolID, p)
		contentType = "text/x-shellscript; charset=utf-8"
		filename = "setup-" + toolID + ".sh"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Content-Disposition", `inline; filename="`+filename+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(script))
}

// writeSetupText answers with Axon's plain-text error shape.
func writeSetupText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
