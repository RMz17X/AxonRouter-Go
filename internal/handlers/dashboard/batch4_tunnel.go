package dashboard

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/handlerutil"
)

// Ngrok tunnel — port of AxonRouter's src/app/api/tunnel/ngrok/start +
// stop routes over packages/tunnel (@ngrok/ngrok SDK upstream). There is
// no Go SDK equivalent in this binary, so the manager drives the operator's
// ngrok agent CLI: start launches `ngrok http <gateway port>` and learns
// the public URL from the agent's local API; stop terminates it. When the
// agent is not installed the status says so honestly (installed: false,
// phase "not_installed") instead of pretending a tunnel exists.
//
// Docker-only tension (flagged for the parent): the tunnel is a host
// process by nature — inside a container this manages the container's own
// ngrok agent and port, which is only useful with host networking.

type ngrokStatus struct {
	Supported bool    `json:"supported"`
	Installed bool    `json:"installed"`
	Running   bool    `json:"running"`
	PublicURL *string `json:"publicUrl"`
	APIURL    *string `json:"apiUrl"`
	TargetURL string  `json:"targetUrl"`
	Phase     string  `json:"phase"`
	LastError *string `json:"lastError"`
}

var ngrokState = struct {
	sync.Mutex
	cmd       *exec.Cmd
	publicURL string
	lastError string
	output    *tailBuffer
}{}

// tailBuffer keeps the last bytes of the agent's output for error reports.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 8192 {
		t.buf = t.buf[len(t.buf)-8192:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func ngrokTargetURL() string {
	port := 20130 // this gateway's default listen port (internal/config)
	if raw := os.Getenv("PORT"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			port = n
		}
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

func ngrokInstalled() bool {
	_, err := exec.LookPath("ngrok")
	return err == nil
}

func ngrokTokenAvailable() bool {
	return strings.TrimSpace(os.Getenv("NGROK_AUTHTOKEN")) != ""
}

// ngrokCurrentStatus assembles the status object (upstream
// getNgrokTunnelStatus), with lastError attached when a start failed —
// upstream attaches it on the start response only, which is the only
// surface this port serves besides stop.
func ngrokCurrentStatus(withError bool) ngrokStatus {
	target := ngrokTargetURL()
	ngrokState.Lock()
	publicURL := ngrokState.publicURL
	lastError := ngrokState.lastError
	ngrokState.Unlock()

	st := ngrokStatus{
		Supported: true,
		Installed: ngrokInstalled(),
		TargetURL: target,
	}
	if publicURL != "" {
		st.Running = true
		st.PublicURL = &publicURL
		apiURL := strings.TrimSuffix(publicURL, "/") + "/v1"
		st.APIURL = &apiURL
		st.Phase = "running"
		return st
	}
	switch {
	case !st.Installed:
		st.Phase = "not_installed"
	case withError && lastError != "":
		st.Phase = "error"
	case ngrokTokenAvailable():
		st.Phase = "stopped"
	default:
		st.Phase = "needs_auth"
	}
	if withError && lastError != "" {
		st.LastError = &lastError
	}
	return st
}

func ngrokSetError(msg string) {
	ngrokState.Lock()
	ngrokState.lastError = msg
	ngrokState.Unlock()
}

// pollNgrokURL waits for the agent API to report a public tunnel URL.
func pollNgrokURL(ctx context.Context) (string, error) {
	deadline := time.Now().Add(20 * time.Second)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:4040/api/tunnels", nil)
		if err == nil {
			resp, err := client.Do(req)
			if err == nil {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				resp.Body.Close()
				var payload struct {
					Tunnels []struct {
						PublicURL string `json:"public_url"`
					} `json:"tunnels"`
				}
				if err := json.Unmarshal(body, &payload); err == nil {
					fallback := ""
					for _, t := range payload.Tunnels {
						if strings.HasPrefix(t.PublicURL, "https://") {
							return t.PublicURL, nil
						}
						if fallback == "" {
							fallback = t.PublicURL
						}
					}
					if fallback != "" {
						return fallback, nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("ngrok did not return a public URL")
}

// startNgrokTunnel mirrors upstream startNgrokTunnel: no-op when running,
// needs_auth without a token, otherwise launch and await the URL.
func startNgrokTunnel(ctx context.Context, inputToken string) ngrokStatus {
	if st := ngrokCurrentStatus(false); st.Running {
		return st
	}
	token := strings.TrimSpace(inputToken)
	if token == "" {
		token = strings.TrimSpace(os.Getenv("NGROK_AUTHTOKEN"))
	}
	if !ngrokInstalled() {
		ngrokSetError("ngrok agent is not installed (no ngrok binary in PATH)")
		return ngrokCurrentStatus(true)
	}
	if token == "" {
		ngrokSetError("An ngrok authtoken is required.")
		st := ngrokCurrentStatus(true)
		st.Phase = "needs_auth"
		return st
	}

	ngrokState.Lock()
	if ngrokState.cmd != nil {
		ngrokState.Unlock()
		return ngrokCurrentStatus(true)
	}
	target := ngrokTargetURL()
	port := target[strings.LastIndex(target, ":")+1:]
	out := &tailBuffer{}
	cmd := exec.Command("ngrok", "http", port, "--log", "stdout")
	cmd.Env = append(os.Environ(), "NGROK_AUTHTOKEN="+token)
	cmd.Stdout = out
	cmd.Stderr = out
	ngrokState.output = out
	ngrokState.lastError = ""
	if err := cmd.Start(); err != nil {
		ngrokState.Unlock()
		ngrokSetError(err.Error())
		return ngrokCurrentStatus(true)
	}
	ngrokState.cmd = cmd
	ngrokState.Unlock()

	go func() { _ = cmd.Wait() }()

	publicURL, err := pollNgrokURL(ctx)
	if err != nil {
		output := ""
		if ngrokState.output != nil {
			output = strings.TrimSpace(ngrokState.output.String())
		}
		msg := err.Error()
		if output != "" {
			lines := strings.Split(output, "\n")
			msg = strings.TrimSpace(lines[len(lines)-1])
			if msg == "" {
				msg = err.Error()
			}
		}
		stopNgrokProcess()
		ngrokSetError(msg)
		return ngrokCurrentStatus(true)
	}
	ngrokState.Lock()
	ngrokState.publicURL = publicURL
	ngrokState.lastError = ""
	ngrokState.Unlock()
	return ngrokCurrentStatus(false)
}

func stopNgrokProcess() {
	ngrokState.Lock()
	cmd := ngrokState.cmd
	ngrokState.cmd = nil
	ngrokState.publicURL = ""
	ngrokState.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// HandleStartNgrokTunnel handles POST /api/tunnel/ngrok/start.
func (h *DashboardHandler) HandleStartNgrokTunnel(w http.ResponseWriter, r *http.Request) {
	body, _ := readGroupBody(r)
	token := ""
	if body != nil {
		token = handlerutil.GetString(body, "authToken")
	}
	status := startNgrokTunnel(r.Context(), token)
	handlerutil.WriteJSON(w, http.StatusOK, status)
}

// HandleStopNgrokTunnel handles POST /api/tunnel/ngrok/stop.
func (h *DashboardHandler) HandleStopNgrokTunnel(w http.ResponseWriter, r *http.Request) {
	stopNgrokProcess()
	ngrokSetError("")
	handlerutil.WriteJSON(w, http.StatusOK, ngrokCurrentStatus(false))
}
