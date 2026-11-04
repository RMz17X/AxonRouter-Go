package media

// Batch 10 item 4 — the video job proxy core, ported from
// AxonRouter open-sse/handlers/videoCore.js. Axon has NO video
// job store: a create POSTs to the provider's video endpoint and
// status/content GETs proxy the same upstream job through the
// connection that created it (account pinning — see
// video_handlers.go). This file is the single-attempt proxy: URL
// construction, auth, the refresh-once-retry-once rule, timeout
// and error sanitization. Rotation across connections lives in
// the create handler, exactly as Axon splits core vs handler.
//
// Provider scope: the providers whose registry entry carries a
// videoConfig in Axon AND a VideoURL in Go's registry — xai and
// openrouter. Axon's remaining videoConfig providers (beatapi,
// vertex, unikey) have no Go VideoURL and stay on the generic
// media lane; vertex additionally has a dedicated Axon adapter
// (videoProviders/vertex.js) that is not ported.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// videoFetchTimeout is Axon's VIDEO_FETCH_TIMEOUT_MS.
const videoFetchTimeout = 120 * time.Second

// videoBodyReadCap bounds a buffered (non-content) upstream body.
// Job JSON and error documents are small; content downloads
// stream and never pass through this cap.
const videoBodyReadCap = 8 << 20

// videoConfigFor reports whether a provider is a video provider
// in the ported scope and returns its registry config.
func videoConfigFor(provider string) (providers.ProviderConfig, bool) {
	cfg, ok := providers.KnownProviders[provider]
	if !ok || cfg.VideoURL == "" {
		return providers.ProviderConfig{}, false
	}
	return cfg, true
}

// videoBaseURL resolves the upstream collection URL for one
// connection: the registry VideoURL, unless the connection's own
// stored baseUrl points at a different host — the operator
// override the media lane already honors (and the shape the
// pinning proof exercises). The derivation mirrors
// forwardMediaRequest's VideoURL branch: strip a trailing /v1
// lane suffix from the stored base, then append /v1/videos.
func videoBaseURL(cfg providers.ProviderConfig, connData *shared.ConnectionData) string {
	base := strings.TrimSuffix(cfg.VideoURL, "/")
	if connData == nil || connData.BaseURL == "" {
		return base
	}
	stored, err := url.Parse(connData.BaseURL)
	if err != nil || stored.Host == "" {
		return base
	}
	if registry, err := url.Parse(cfg.VideoURL); err == nil && registry.Host == stored.Host {
		return base
	}
	raw := strings.TrimRight(connData.BaseURL, "/")
	return strings.TrimSuffix(raw, "/v1") + "/v1/videos"
}

// buildVideoUpstreamURL mirrors Axon's buildUpstreamUrl plus the
// OpenRouter adapter's plan: a request id addresses the job
// ({base}/{id}, content at {base}/{id}/content); a create posts
// to {base}/{action} — except OpenRouter, whose adapter posts to
// the collection root.
func buildVideoUpstreamURL(provider, base, action, requestID string, content bool) string {
	if requestID != "" {
		u := base + "/" + url.PathEscape(requestID)
		if content {
			u += "/content"
		}
		return u
	}
	if provider == "openrouter" {
		return base
	}
	return base + "/" + action
}

var videoBearerPattern = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._\-+/=]+`)

// sanitizeVideoSecrets mirrors Axon's sanitizeSecrets: Bearer
// tokens and the literal credential values are replaced with
// [redacted] before an upstream message can reach a client.
func sanitizeVideoSecrets(text string, secrets ...string) string {
	out := videoBearerPattern.ReplaceAllString(text, "Bearer [redacted]")
	for _, secret := range secrets {
		if len(secret) >= 8 {
			out = strings.ReplaceAll(out, secret, "[redacted]")
		}
	}
	return out
}

// videoUpstreamResponse is one core attempt's outcome. Exactly
// one shape is populated: a failure (ErrText, with Status), a
// buffered success (Body), or a streaming content success
// (Stream — the caller owns closing it).
type videoUpstreamResponse struct {
	Status             int
	ContentType        string
	Body               []byte
	Stream             io.ReadCloser
	ContentLength      int64
	ContentDisposition string
	ErrText            string
}

// videoProxyParams describes one proxied video call.
type videoProxyParams struct {
	Action         string // generations | edits | extensions (create only)
	RequestID      string // "" = create
	Content        bool   // content download (GET only)
	Body           []byte // final create body (model already stripped)
	ContentType    string // incoming content type (create)
	IdempotencyKey string
}

// videoProxy performs ONE upstream attempt for a video call
// through the given connection — Axon's handleVideoProxyCore,
// including its refresh rule: a 401/403 with a refreshable
// connection refreshes the token once and retries once. It never
// re-sends a create on a network error (the job may exist
// upstream); rotation decisions belong to the caller.
func (h *MediaHandler) videoProxy(ctx context.Context, provider string, cfg providers.ProviderConfig, conn *models.ProviderConnection, connData *shared.ConnectionData, p videoProxyParams) *videoUpstreamResponse {
	token := connData.AccessToken
	if token == "" {
		token = connData.APIKey
	}
	if token == "" {
		token = cfg.DefaultAPIKey
	}
	base := videoBaseURL(cfg, connData)
	target := buildVideoUpstreamURL(provider, base, p.Action, p.RequestID, p.Content)

	client, err := h.ChatH.GetClientForConnection(connData)
	if err != nil {
		return &videoUpstreamResponse{Status: http.StatusBadGateway,
			ErrText: fmt.Sprintf("[%s] %s", provider, sanitizeVideoSecrets(err.Error(), token))}
	}

	res := h.videoFetch(ctx, client, provider, target, token, cfg, p)
	if (res.Status == http.StatusUnauthorized || res.Status == http.StatusForbidden) && conn != nil {
		if newToken, _, rerr := h.ChatH.ForceRefreshOAuthToken(conn.ID); rerr == nil && newToken != "" && newToken != token {
			return h.videoFetch(ctx, client, provider, target, newToken, cfg, p)
		}
	}
	return res
}

// cancelOnCloseReadCloser ties the fetch timeout context to a
// streamed response body: the context must outlive videoFetch
// (the caller copies the stream afterwards) but must not outlive
// the body itself.
type cancelOnCloseReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnCloseReadCloser) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// videoFetch executes the HTTP exchange for one token. A non-2xx
// upstream answer is a populated ErrText result (status
// preserved), not a Go error — Axon classifies statuses in the
// handler, so the core reports them.
func (h *MediaHandler) videoFetch(ctx context.Context, client *http.Client, provider, target, token string, cfg providers.ProviderConfig, p videoProxyParams) *videoUpstreamResponse {
	reqCtx, cancel := context.WithTimeout(ctx, videoFetchTimeout)
	// The streaming content path hands the context to the body
	// (see below); every other path releases it on return.
	streaming := false
	defer func() {
		if !streaming {
			cancel()
		}
	}()

	method := http.MethodPost
	if p.RequestID != "" {
		method = http.MethodGet
	}
	var bodyReader io.Reader
	if method == http.MethodPost && p.Body != nil {
		bodyReader = bytes.NewReader(p.Body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, target, bodyReader)
	if err != nil {
		return &videoUpstreamResponse{Status: http.StatusBadGateway,
			ErrText: fmt.Sprintf("[%s] %s", provider, sanitizeVideoSecrets(err.Error(), token))}
	}
	if p.Content {
		req.Header.Set("Accept", "*/*")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range cfg.StaticHeaders {
		req.Header.Set(k, v)
	}
	if method == http.MethodPost {
		if p.ContentType != "" {
			req.Header.Set("Content-Type", p.ContentType)
		}
		if p.IdempotencyKey != "" {
			req.Header.Set("Idempotency-Key", p.IdempotencyKey)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) || reqCtx.Err() == context.DeadlineExceeded {
			status = http.StatusRequestTimeout
		}
		return &videoUpstreamResponse{Status: status,
			ErrText: fmt.Sprintf("[%s] video upstream fetch failed: %s", provider, sanitizeVideoSecrets(err.Error(), token))}
	}

	if p.Content && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "video/mp4"
		}
		streaming = true
		return &videoUpstreamResponse{
			Status:             resp.StatusCode,
			ContentType:        contentType,
			Stream:             &cancelOnCloseReadCloser{ReadCloser: resp.Body, cancel: cancel},
			ContentLength:      resp.ContentLength,
			ContentDisposition: resp.Header.Get("Content-Disposition"),
		}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, videoBodyReadCap))

	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(sanitizeVideoSecrets(string(body), token))
		if len(msg) > 2000 {
			msg = msg[:2000]
		}
		if msg == "" {
			msg = fmt.Sprintf("video upstream error (HTTP %d)", resp.StatusCode)
		}
		return &videoUpstreamResponse{Status: resp.StatusCode,
			ErrText: fmt.Sprintf("[%s] %s", provider, msg)}
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	return &videoUpstreamResponse{Status: resp.StatusCode, ContentType: contentType, Body: body}
}
