package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// claude.ai's transport. The site sits behind Cloudflare, which rejects
// Go's native TLS fingerprint even with a valid sessionKey — the same
// pure-Go impersonating stack the grok-web / deepseek-web / gemini-web
// providers use (AGENTS.md section 4.F), here with the Chrome_133
// profile so the ClientHello matches the Chrome/133 User-Agent the
// provider spec pins.

var claudeWebTLSProfile = profiles.Chrome_133

// claudeWebBaseURL is the upstream every claude.ai API call goes to;
// var so the test suite can aim it at a fake site.
var claudeWebBaseURL = "https://claude.ai"

// claudeWebOrigin is the web app origin the API expects in Origin/Referer.
const claudeWebOrigin = "https://claude.ai"

// claudeWebDoer issues one claude.ai request. The interface exists
// because the test suite drives the whole flow against a fake site;
// production uses the browser-fidelity client below.
type claudeWebDoer interface {
	do(ctx context.Context, method, target string, headers [][2]string, body []byte) (*http.Response, error)
}

// claudeWebTransportFactory builds the doer for one request. Var so the
// tests can swap in a plain client aimed at a fake site.
var claudeWebTransportFactory = func(req *Request) (claudeWebDoer, error) {
	if claudeWebPlainHTTP() {
		return &claudeWebPlainDoer{}, nil
	}
	options := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(0),
		tls_client.WithClientProfile(claudeWebTLSProfile),
		tls_client.WithRandomTLSExtensionOrder(),
		tls_client.WithNotFollowRedirects(),
		tls_client.WithDialer(net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}),
	}
	proxyURL := stealthProxyFrom(req.Client, claudeWebBaseURL)
	if proxyURL == "" {
		if p, _ := req.ConnData["proxyUrl"].(string); strings.TrimSpace(p) != "" {
			proxyURL = strings.TrimSpace(p)
		}
	}
	if proxyURL != "" {
		options = append(options, tls_client.WithProxyUrl(proxyURL))
	}
	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), options...)
	if err != nil {
		return nil, fmt.Errorf("claude-web: build TLS client: %w", err)
	}
	return &claudeWebTLSDoer{client: client}, nil
}

// claudeWebTLSDoer runs requests through the impersonating client,
// writing the headers in the order given — header order is part of the
// fingerprint, and Go's net/http would sort them.
type claudeWebTLSDoer struct {
	client tls_client.HttpClient
}

func (d *claudeWebTLSDoer) do(ctx context.Context, method, target string, headers [][2]string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := fhttp.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, fmt.Errorf("claude-web: build request: %w", err)
	}
	values := make(map[string]string, len(headers))
	order := make([]string, 0, len(headers))
	for _, pair := range headers {
		values[pair[0]] = pair[1]
		if !slices.Contains(order, pair[0]) {
			order = append(order, pair[0])
		}
	}
	stealthSetOrdered(request.Header, order, values)
	request.Header[fhttp.PHeaderOrderKey] = stealthPseudoHeaderOrder

	response, err := d.client.Do(request)
	if err != nil {
		return nil, err
	}
	header := make(http.Header, len(response.Header))
	for name, vals := range response.Header {
		header[name] = vals
	}
	return &http.Response{
		StatusCode:    response.StatusCode,
		Status:        response.Status,
		Header:        header,
		Body:          response.Body,
		ContentLength: response.ContentLength,
	}, nil
}

// claudeWebPlainDoer answers through net/http. It is the test and
// escape-hatch path (CLAUDE_WEB_PLAIN_HTTP=1), the same switch the
// sibling web providers carry.
type claudeWebPlainDoer struct{}

func (d *claudeWebPlainDoer) do(ctx context.Context, method, target string, headers [][2]string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, fmt.Errorf("claude-web: build request: %w", err)
	}
	for _, pair := range headers {
		request.Header.Set(pair[0], pair[1])
	}
	return http.DefaultClient.Do(request)
}

func claudeWebPlainHTTP() bool {
	return os.Getenv("CLAUDE_WEB_PLAIN_HTTP") == "1"
}

// claudeWebHeaders builds the browser-identity header set the spec
// pins for one claude.ai call. accept differs per call: JSON for the
// REST calls, the SSE accept for the completion stream.
func claudeWebHeaders(cred claudeWebCred, accept string) [][2]string {
	return [][2]string{
		{"Accept", accept},
		{"Accept-Language", "en-US,en;q=0.9"},
		{"Content-Type", "application/json"},
		{"Cookie", cred.cookieHeader},
		{"Origin", claudeWebOrigin},
		{"Referer", claudeWebOrigin + "/"},
		{"User-Agent", cred.userAgent},
		{"sec-ch-ua", `"Not(A:Brand";v="99", "Chromium";v="133", "Google Chrome";v="133"`},
		{"sec-ch-ua-mobile", "?0"},
		{"sec-ch-ua-platform", claudeWebPlatformForUA(cred.userAgent)},
		{"sec-fetch-dest", "empty"},
		{"sec-fetch-mode", "cors"},
		{"sec-fetch-site", "same-origin"},
	}
}

// claudeWebPlatformForUA derives the sec-ch-ua-platform value from the
// connection's User-Agent so the pair stays consistent when Chroma
// syncs a non-Linux browser profile.
func claudeWebPlatformForUA(ua string) string {
	switch {
	case strings.Contains(ua, "Windows"):
		return `"Windows"`
	case strings.Contains(ua, "Macintosh"), strings.Contains(ua, "Mac OS"):
		return `"macOS"`
	default:
		return `"Linux"`
	}
}
