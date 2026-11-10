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

// kimi.moonshot.cn's transport: the same pure-Go impersonating TLS
// stack the sibling web providers use (AGENTS.md section 4.F), with
// the Chrome_133 profile so the ClientHello matches the Chrome/133
// User-Agent the provider spec pins.

var kimiWebTLSProfile = profiles.Chrome_133

// kimiWebBaseURL is the upstream every Kimi Web API call goes to; var
// so the test suite can aim it at a fake site.
var kimiWebBaseURL = "https://kimi.moonshot.cn"

// kimiWebOrigin is the web app origin the API expects in Origin/Referer.
const kimiWebOrigin = "https://kimi.moonshot.cn"

// kimiWebDoer issues one kimi.moonshot.cn request. The interface
// exists because the test suite drives the whole flow against a fake
// site; production uses the browser-fidelity client below.
type kimiWebDoer interface {
	do(ctx context.Context, method, target string, headers [][2]string, body []byte) (*http.Response, error)
}

// kimiWebTransportFactory builds the doer for one request. Var so the
// tests can swap in a plain client aimed at a fake site.
var kimiWebTransportFactory = func(req *Request) (kimiWebDoer, error) {
	if kimiWebPlainHTTP() {
		return &kimiWebPlainDoer{}, nil
	}
	options := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(0),
		tls_client.WithClientProfile(kimiWebTLSProfile),
		tls_client.WithRandomTLSExtensionOrder(),
		tls_client.WithNotFollowRedirects(),
		tls_client.WithDialer(net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}),
	}
	proxyURL := stealthProxyFrom(req.Client, kimiWebBaseURL)
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
		return nil, fmt.Errorf("kimi-web: build TLS client: %w", err)
	}
	return &kimiWebTLSDoer{client: client}, nil
}

// kimiWebTLSDoer runs requests through the impersonating client,
// writing the headers in the order given — header order is part of
// the fingerprint, and Go's net/http would sort them.
type kimiWebTLSDoer struct {
	client tls_client.HttpClient
}

func (d *kimiWebTLSDoer) do(ctx context.Context, method, target string, headers [][2]string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := fhttp.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, fmt.Errorf("kimi-web: build request: %w", err)
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

// kimiWebPlainDoer answers through net/http. It is the test and
// escape-hatch path (KIMI_WEB_PLAIN_HTTP=1), the same switch the
// sibling web providers carry.
type kimiWebPlainDoer struct{}

func (d *kimiWebPlainDoer) do(ctx context.Context, method, target string, headers [][2]string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, fmt.Errorf("kimi-web: build request: %w", err)
	}
	for _, pair := range headers {
		request.Header.Set(pair[0], pair[1])
	}
	return http.DefaultClient.Do(request)
}

func kimiWebPlainHTTP() bool {
	return os.Getenv("KIMI_WEB_PLAIN_HTTP") == "1"
}

// kimiWebHeaders builds the browser-identity header set the spec pins
// for one Kimi Web call, carrying the given bearer token (the refresh
// token for the refresh call, the access token for everything else).
func kimiWebHeaders(cred kimiWebCred, bearer string) [][2]string {
	return [][2]string{
		{"Accept", "application/json, text/plain, */*"},
		{"Accept-Language", "zh-CN,zh;q=0.9,en-US;q=0.8,en;q=0.7"},
		{"Authorization", "Bearer " + bearer},
		{"Content-Type", "application/json"},
		{"Origin", kimiWebOrigin},
		{"Referer", kimiWebOrigin + "/"},
		{"User-Agent", cred.userAgent},
		{"sec-ch-ua", `"Not(A:Brand";v="99", "Chromium";v="133", "Google Chrome";v="133"`},
		{"sec-ch-ua-mobile", "?0"},
		{"sec-ch-ua-platform", claudeWebPlatformForUA(cred.userAgent)},
		{"sec-fetch-dest", "empty"},
		{"sec-fetch-mode", "cors"},
		{"sec-fetch-site", "same-origin"},
	}
}
