package smart

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"9router/proxy/internal/db"
	internalproxy "9router/proxy/internal/proxy"
)

const (
	DefaultCanaryInterval = 10 * time.Minute
	DefaultProbeTimeout  = 12 * time.Second
	CanaryProbeTarget     = "https://www.cloudflare.com/cdn-cgi/trace"
	MaxProbeFailures      = 10
)

// CanaryWorker manages periodic background health probes for active proxy pools.
type CanaryWorker struct {
	repo     *db.Repo
	tracker  *FitnessTracker
	interval time.Duration
	stopCh   chan struct{}
}

// NewCanaryWorker creates a new background canary probe worker.
func NewCanaryWorker(repo *db.Repo, tracker *FitnessTracker) *CanaryWorker {
	if tracker == nil {
		tracker = DefaultTracker
	}
	return &CanaryWorker{
		repo:     repo,
		tracker:  tracker,
		interval: DefaultCanaryInterval,
		stopCh:   make(chan struct{}),
	}
}

// Start launches the background probe loop in a goroutine with optional context.
func (w *CanaryWorker) Start(ctx ...context.Context) {
	if w.repo == nil {
		return
	}
	var parentCtx context.Context
	if len(ctx) > 0 && ctx[0] != nil {
		parentCtx = ctx[0]
	}
	go w.loop(parentCtx)
}

// Stop cleanly stops the background probe worker.
func (w *CanaryWorker) Stop() {
	select {
	case <-w.stopCh:
		return
	default:
		close(w.stopCh)
	}
}

func (w *CanaryWorker) loop(parentCtx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	var doneCh <-chan struct{}
	if parentCtx != nil {
		doneCh = parentCtx.Done()
	}

	// Initial probe pass after short startup delay (10s)
	select {
	case <-time.After(10 * time.Second):
		w.ProbeAll(context.Background())
	case <-doneCh:
		return
	case <-w.stopCh:
		return
	}

	for {
		select {
		case <-ticker.C:
			w.ProbeAll(context.Background())
		case <-doneCh:
			return
		case <-w.stopCh:
			return
		}
	}
}

// ProbeAll tests all active proxy pools and applies the 10x failure disable policy.
func (w *CanaryWorker) ProbeAll(ctx context.Context) {
	if w.repo == nil {
		return
	}
	activeIDs := w.repo.ActivePoolIDs()
	if len(activeIDs) == 0 {
		return
	}

	for _, id := range activeIDs {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		default:
		}

		pool, err := w.repo.GetProxyPool(id)
		if err != nil || pool == nil || !pool.IsActive {
			continue
		}

		ok, latency, err := w.ProbePool(ctx, pool)
		fails := w.tracker.RecordProbe(pool.ID, ok, latency)

		status := "passed"
		if !ok {
			status = "failed"
		}
		_ = w.repo.SetProxyPoolStatus(pool.ID, status, latency.Milliseconds())

		if !ok && fails >= MaxProbeFailures {
			slog.Warn("proxy canary: disabling pool after consecutive probe failures",
				"poolId", pool.ID,
				"name", pool.Name,
				"fails", fails,
				"lastError", err,
			)
			_ = w.repo.DisableProxyPool(pool.ID)
		}
	}
}

// ProbePool tests a single pool against Cloudflare's /cdn-cgi/trace endpoint.
func (w *CanaryWorker) ProbePool(ctx context.Context, pool *db.ProxyPool) (bool, time.Duration, error) {
	if pool == nil || len(pool.URLs) == 0 {
		return false, 0, fmt.Errorf("no urls in pool")
	}
	rawURL := pool.NextURL()
	if rawURL == "" {
		return false, 0, fmt.Errorf("empty url in pool")
	}

	probeCtx, cancel := context.WithTimeout(ctx, DefaultProbeTimeout)
	defer cancel()

	start := time.Now()
	var client *http.Client

	if pool.IsEdgeRelay() {
		// Edge Relay (Cloudflare Workers, Vercel, Deno, Netlify)
		relayTransport := internalproxy.RelayTransport(rawURL, &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		})
		client = &http.Client{
			Transport: relayTransport,
			Timeout:   DefaultProbeTimeout,
		}
	} else {
		// HTTP / HTTPS CONNECT proxy
		parsedURL, err := url.Parse(rawURL)
		if err != nil {
			return false, 0, fmt.Errorf("parse proxy url %q: %w", rawURL, err)
		}
		transport := internalproxy.NewSafeTransportForProxy(parsedURL.String())
		client = &http.Client{
			Transport: transport,
			Timeout:   DefaultProbeTimeout,
		}
	}

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, CanaryProbeTarget, nil)
	if err != nil {
		return false, 0, err
	}
	req.Header.Set("User-Agent", "9router-canary/1.0")

	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return false, latency, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, latency, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return false, latency, err
	}

	bodyStr := string(body)
	if strings.Contains(bodyStr, "fl=") || strings.Contains(bodyStr, "ip=") || strings.Contains(bodyStr, "h=") {
		return true, latency, nil
	}
	return false, latency, fmt.Errorf("trace response signature missing")
}
