package chat

// Batch 6 — POST /api/translator/send, ported from AxonRouter
// src/app/api/translator/send/route.ts. The dashboard's translator lab
// sends a provider-format body through the provider's executor on the
// gateway's chosen active connection and streams the upstream answer
// back. Error mapping mirrors Axon: missing input → 400
// {success:false, error:"provider, model, and body required"}; no
// connection → 400; upstream failure → {success:false,
// error:"Provider error: <status>", details} at the upstream status;
// success → the raw upstream body with event-stream headers (Axon sets
// them unconditionally). A 401/403 triggers one OAuth refresh + retry,
// like Axon's refreshTokenByProvider path — here the refresh is the
// chat pipeline's own forceRefreshOAuthToken, and executors also
// self-refresh when a connection id is set (noted in .changes/917).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/proxy/executor"
)

// translatorSendCapture buffers an executor response until its status
// is known: a 2xx switches to live passthrough (the caller has already
// applied Axon's event-stream headers at that point), anything else
// stays buffered so the handler can replace it with Axon's error JSON.
type translatorSendCapture struct {
	w       http.ResponseWriter
	header  http.Header
	status  int
	buf     bytes.Buffer
	live    bool
	decided bool
}

func newTranslatorSendCapture(w http.ResponseWriter) *translatorSendCapture {
	return &translatorSendCapture{w: w, header: make(http.Header)}
}

func (c *translatorSendCapture) Header() http.Header { return c.header }

func (c *translatorSendCapture) WriteHeader(status int) {
	if c.decided {
		return
	}
	c.decided = true
	c.status = status
	if status >= 200 && status < 300 {
		c.live = true
		dst := c.w.Header()
		dst.Set("Content-Type", "text/event-stream")
		dst.Set("Cache-Control", "no-cache")
		dst.Set("Connection", "keep-alive")
		c.w.WriteHeader(status)
	}
}

func (c *translatorSendCapture) Write(p []byte) (int, error) {
	if !c.decided {
		c.WriteHeader(http.StatusOK)
	}
	if c.live {
		return c.w.Write(p)
	}
	return c.buf.Write(p)
}

func (c *translatorSendCapture) Flush() {
	if c.live {
		if f, ok := c.w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func (c *translatorSendCapture) statusCode() int {
	if c.status == 0 {
		return http.StatusOK
	}
	return c.status
}

// HandleTranslatorSend implements POST /api/translator/send.
func (h *ChatHandler) HandleTranslatorSend(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, message string) {
		handlerutil.WriteJSON(w, status, map[string]any{
			"success": false,
			"error":   message,
		})
	}

	var reqBody struct {
		Provider string          `json:"provider"`
		Model    string          `json:"model"`
		Body     json.RawMessage `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		fail(http.StatusInternalServerError, err.Error())
		return
	}
	if reqBody.Provider == "" || reqBody.Model == "" || len(reqBody.Body) == 0 || string(reqBody.Body) == "null" {
		fail(http.StatusBadRequest, "provider, model, and body required")
		return
	}
	provider, model := reqBody.Provider, reqBody.Model

	stream := true
	var bodyProbe map[string]any
	if err := json.Unmarshal(reqBody.Body, &bodyProbe); err == nil {
		if v, ok := bodyProbe["stream"].(bool); ok {
			stream = v
		}
	}

	conn, connData, err := h.GetBestConnection(provider, "", nil, model)
	if err != nil || conn == nil || connData == nil {
		fail(http.StatusBadRequest, fmt.Sprintf("No active connection for provider: %s", provider))
		return
	}

	run := func(apiKey string) (*translatorSendCapture, error) {
		providerCfg, err := h.getProviderConfig(provider, connData)
		if err != nil {
			return nil, err
		}
		httpClient, err := h.getClientForConnection(connData)
		if err != nil {
			return nil, err
		}
		exec := executor.Get(provider)
		if exec == nil {
			exec = executor.Default()
		}
		execReq := &executor.Request{
			Ctx:          r.Context(),
			Client:       httpClient,
			Config:       providerCfg,
			Provider:     provider,
			APIKey:       apiKey,
			Body:         reqBody.Body,
			IsStream:     stream,
			ConnectionID: conn.ID,
			ModelName:    model,
			StartTime:    time.Now(),
		}
		if len(connData.ProviderSpecificData) > 0 {
			execReq.ConnData = connData.ProviderSpecificData
		}
		if h.Repo != nil {
			execReq.Leases = h.Repo
		}
		capture := newTranslatorSendCapture(w)
		execErr := exec(capture, execReq)
		execReq.Release()
		return capture, execErr
	}

	apiKey := extractAPIKey(connData)
	capture, execErr := run(apiKey)
	if execErr != nil && !capture.decided {
		fail(http.StatusInternalServerError, execErr.Error())
		return
	}
	status := capture.statusCode()

	if (status == http.StatusUnauthorized || status == http.StatusForbidden) && conn.ID != "" {
		if refreshedKey, _, rErr := h.forceRefreshOAuthToken(conn.ID); rErr == nil &&
			refreshedKey != "" && refreshedKey != apiKey {
			if retryCapture, retryErr := run(refreshedKey); retryErr == nil || retryCapture.decided {
				capture, status = retryCapture, retryCapture.statusCode()
			}
		}
	}

	if status < 200 || status >= 300 {
		handlerutil.WriteJSON(w, status, map[string]any{
			"success": false,
			"error":   fmt.Sprintf("Provider error: %d", status),
			"details": capture.buf.String(),
		})
		return
	}
	// Success: the capture already streamed the upstream body through
	// with Axon's event-stream headers. An executor that wrote nothing
	// at all still owes the client those headers and a 200.
	if !capture.decided {
		dst := w.Header()
		dst.Set("Content-Type", "text/event-stream")
		dst.Set("Cache-Control", "no-cache")
		dst.Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
	}
}
