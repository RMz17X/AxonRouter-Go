package oauth

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"

	"9router/proxy/internal/handlerutil"
)

// The loopback-proxy GET actions of the generic dispatch route (split from
// batch3_dynamic.go): start-proxy, poll-status, stop-proxy, ide-status.
// The proxy listeners and session registries themselves live in
// batch3_proxy*.go.

// ---- start-proxy / poll-status / stop-proxy ----

func (h *OAuthHandler) dynStartProxy(w http.ResponseWriter, r *http.Request, provider string) {
	q := r.URL.Query()
	switch provider {
	case "trae", "windsurf":
		ok, port, callbackURL, reason := dynProxyFor(provider).start(h, 0)
		if !ok {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": false, "reason": reason})
			return
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "port": port, "callbackUrl": callbackURL})
		return
	case "zed":
		preferred := zedDefaultPort
		if v := q.Get("native_app_port"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				preferred = n
			} else {
				preferred = 0
			}
		}
		ok, port, callbackURL, reason := zedDynProxy.start(h, preferred)
		if !ok {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": false, "reason": reason})
			return
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "port": port, "callbackUrl": callbackURL})
		return
	case "xiaomi-mimo":
		ok, port, callbackURL, reason := mimoDynProxy.start()
		if !ok {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": false, "reason": reason})
			return
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "port": port, "callbackUrl": callbackURL})
		return
	}
	if provider != "codex" && provider != "xai" {
		axonError(w, http.StatusBadRequest, "Proxy only supported for codex/xai/trae/windsurf/zed")
		return
	}
	appPort := q.Get("app_port")
	if appPort == "" {
		axonError(w, http.StatusBadRequest, "Missing app_port")
		return
	}
	if provider == "codex" {
		// The dedicated codex start-proxy route owns this URL in this
		// codebase (static segment wins over {provider}); delegate so the
		// dispatch table stays total if routing ever changes.
		status, captured := runCaptured(func(cw http.ResponseWriter) { h.HandleCodexStartProxy(cw, r) })
		if captured == nil {
			captured = map[string]any{"success": false, "reason": "start_failed"}
		}
		handlerutil.WriteJSON(w, status, captured)
		return
	}
	ok, reason := xaiLoopback.start(h, appPort)
	serverSide := false
	if ok && q.Get("state") != "" && q.Get("code_verifier") != "" && q.Get("redirect_uri") != "" {
		serverSide = xaiLoopback.register(q.Get("state"), q.Get("code_verifier"), q.Get("redirect_uri"))
	}
	resp := map[string]any{"success": ok, "serverSide": serverSide}
	if !ok {
		resp["reason"] = reason
	}
	handlerutil.WriteJSON(w, http.StatusOK, resp)
}

func (h *OAuthHandler) dynPollStatus(w http.ResponseWriter, r *http.Request, provider string) {
	state := r.URL.Query().Get("state")
	if state == "" {
		axonError(w, http.StatusBadRequest, "Missing state")
		return
	}
	switch provider {
	case "codex":
		sess, ok := codexLoopback.lookup(state)
		if !ok {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "unknown"})
			return
		}
		if sess.status != "done" && sess.status != "error" {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": sess.status})
			return
		}
		payload := map[string]any{
			"codeVerifier": sess.codeVerifier, "redirectUri": sess.redirectURI,
			"status": sess.status, "createdAt": sess.createdAt.UnixMilli(),
		}
		if sess.status == "done" {
			payload["connectionId"] = sess.connectionID
			payload["email"] = sess.email
		} else {
			payload["error"] = sess.errMsg
		}
		codexLoopback.clear(state)
		handlerutil.WriteJSON(w, http.StatusOK, payload)
	case "xai":
		sess, ok := xaiLoopback.lookup(state)
		if !ok {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "unknown"})
			return
		}
		if sess.status != "done" && sess.status != "error" {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": sess.status})
			return
		}
		payload := map[string]any{
			"codeVerifier": sess.codeVerifier, "redirectUri": sess.redirectURI,
			"status": sess.status, "createdAt": sess.createdAt.UnixMilli(),
		}
		if sess.status == "done" {
			payload["connectionId"] = sess.connectionID
			payload["email"] = sess.email
		} else {
			payload["error"] = sess.errMsg
		}
		xaiLoopback.clear(state)
		handlerutil.WriteJSON(w, http.StatusOK, payload)
	case "trae", "windsurf", "zed":
		p := dynProxyFor(provider)
		sess, ok := p.get(state)
		if !ok {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "unknown"})
			return
		}
		if sess.status != "done" && sess.status != "error" {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": sess.status})
			return
		}
		payload := map[string]any{
			"state": sess.state, "status": sess.status,
			"createdAt": sess.createdAt.UnixMilli(),
		}
		if provider == "zed" {
			payload["codeVerifier"] = sess.codeVerifier
			payload["systemId"] = nullOr(sess.systemID)
		}
		if sess.status == "done" {
			payload["connectionId"] = sess.connectionID
			payload["email"] = sess.email
		} else {
			payload["error"] = sess.errMsg
		}
		p.clear(state)
		handlerutil.WriteJSON(w, http.StatusOK, payload)
	case "xiaomi-mimo":
		sess, ok := mimoDynProxy.lookup(state)
		if !ok {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "unknown"})
			return
		}
		if sess.status != "done" && sess.status != "error" {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": sess.status})
			return
		}
		if sess.status == "error" {
			payload := map[string]any{"status": "error", "result": nil, "error": sess.errMsg}
			mimoDynProxy.clear(state)
			mimoDynProxy.stop()
			handlerutil.WriteJSON(w, http.StatusOK, payload)
			return
		}
		// A finished mimo session survives until the client POSTs exchange.
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"status": "done",
			"result": map[string]any{
				"uid": nullOr(sess.uid), "accessToken": sess.sk, "baseUrl": sess.baseURL,
			},
			"error": nil,
		})
	default:
		axonError(w, http.StatusBadRequest, "Poll only supported for codex/xai/trae/windsurf/zed/xiaomi-mimo")
	}
}

func dynStopProxy(w http.ResponseWriter, provider string) {
	switch provider {
	case "trae", "windsurf", "zed":
		dynProxyFor(provider).stop()
	case "xai":
		xaiLoopback.stop()
	case "codex":
		codexLoopback.stop()
	case "xiaomi-mimo":
		mimoDynProxy.stop()
	default:
		axonError(w, http.StatusBadRequest, "Proxy only supported for codex/xai/trae/windsurf/zed/xiaomi-mimo")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// ---- ide-status ----

// ideInstallPaths is Axon's IDE_PATHS table (utils/ideDetect.js).
func ideInstallPaths(provider string) ([]string, string) {
	switch provider {
	case "trae":
		switch runtime.GOOS {
		case "darwin":
			return []string{"/Applications/Trae.app"}, "trae"
		case "windows":
			return []string{
				filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Trae", "Trae.exe"),
				filepath.Join(os.Getenv("ProgramFiles"), "Trae", "Trae.exe"),
			}, "trae"
		default:
			return []string{"/usr/bin/trae", "/usr/local/bin/trae", "/opt/trae", "/opt/Trae"}, "trae"
		}
	case "windsurf":
		switch runtime.GOOS {
		case "darwin":
			return []string{"/Applications/Windsurf.app"}, "windsurf"
		case "windows":
			return []string{
				filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Windsurf", "Windsurf.exe"),
				filepath.Join(os.Getenv("ProgramFiles"), "Windsurf", "Windsurf.exe"),
			}, "windsurf"
		default:
			return []string{"/usr/bin/windsurf", "/usr/local/bin/windsurf", "/opt/windsurf", "/opt/Windsurf"}, "windsurf"
		}
	}
	return nil, ""
}

func dynIDEStatus(w http.ResponseWriter, provider string) {
	if provider != "trae" && provider != "windsurf" {
		axonError(w, http.StatusBadRequest, "ide-status only supported for trae/windsurf")
		return
	}
	paths, bin := ideInstallPaths(provider)
	for _, p := range paths {
		if p != "" && p != "Trae.exe" && p != "Windsurf.exe" {
			if _, err := os.Stat(p); err == nil {
				handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"installed": true, "path": p})
				return
			}
		}
	}
	if _, err := exec.LookPath(bin); err == nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"installed": true, "path": bin})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"installed": false, "path": nil})
}
