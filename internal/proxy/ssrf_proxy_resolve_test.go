package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// withProxyNameResolution swaps the resolver seam for one test and resets the
// resolution cache, so each test starts without another test's stale answers.
// answers maps a bare proxy name to its IP; a name absent from the map is an
// NXDOMAIN.
func withProxyNameResolution(t *testing.T, answers map[string]string) {
	t.Helper()
	original := lookupIPAddr
	t.Cleanup(func() { lookupIPAddr = original })
	lookupIPAddr = func(_ context.Context, name string) ([]net.IPAddr, error) {
		if ip, ok := answers[name]; ok {
			return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	resetProxyResolutionCache(t)
}

// resetProxyResolutionCache empties the proxy-resolution cache and restores it
// at test end, so the TTL cannot leak one test's answers into another.
func resetProxyResolutionCache(t *testing.T) {
	t.Helper()
	resolverMu.Lock()
	saved := make(map[string]proxyResolution, len(resolverCache))
	for k, v := range resolverCache {
		saved[k] = v
	}
	resolverCache = map[string]proxyResolution{}
	resolverMu.Unlock()
	t.Cleanup(func() {
		resolverMu.Lock()
		resolverCache = saved
		resolverMu.Unlock()
	})
}

// TestSafeDialerMatchesAHostnameProxyByResolution is the reported case: the
// operator names the proxy "hatch-egress-proxy:3128" and Podman DNS answers
// 198.19.0.1 (RFC 2544), so the string allowlist never matched and every
// proxied dial was refused. The guard now resolves the configured name and
// exempts that address.
func TestSafeDialerMatchesAHostnameProxyByResolution(t *testing.T) {
	withProxyResolver(t, "")

	t.Run("a hostname proxy is matched by its resolved address", func(t *testing.T) {
		withProxyNameResolution(t, map[string]string{"hatch-egress-proxy": "198.19.0.1"})
		dialer := newSafeDialerForProxy(newProxyAllowlist("hatch-egress-proxy:3128"))
		if err := dialer.ControlContext(context.Background(), "tcp", "198.19.0.1:3128", nil); err != nil {
			t.Errorf("ControlContext(resolved proxy) = %v, want nil", err)
		}
	})

	t.Run("a hostname proxy resolved elsewhere is still blocked", func(t *testing.T) {
		// The exemption is the proxy's own address, not the reserved range it
		// happens to sit in: another name answering the same IP stays refused.
		withProxyNameResolution(t, map[string]string{"other-proxy": "198.19.0.1"})
		dialer := newSafeDialerForProxy(newProxyAllowlist("other-proxy:3128"))
		if err := dialer.ControlContext(context.Background(), "tcp", "198.19.0.1:9000", nil); !errors.Is(err, ErrSSRFBlocked) {
			t.Errorf("ControlContext(unrelated port on resolved IP) = %v, want ErrSSRFBlocked", err)
		}
	})

	t.Run("a hostname proxy that resolves to another address is blocked", func(t *testing.T) {
		withProxyNameResolution(t, map[string]string{"hatch-egress-proxy": "198.19.0.1"})
		dialer := newSafeDialerForProxy(newProxyAllowlist("hatch-egress-proxy:3128"))
		if err := dialer.ControlContext(context.Background(), "tcp", "10.0.0.7:3128", nil); !errors.Is(err, ErrSSRFBlocked) {
			t.Errorf("ControlContext(10.0.0.7:3128) = %v, want ErrSSRFBlocked", err)
		}
	})

	t.Run("a hostname proxy that does not resolve is blocked", func(t *testing.T) {
		withProxyNameResolution(t, map[string]string{})
		dialer := newSafeDialerForProxy(newProxyAllowlist("missing-proxy:3128"))
		if err := dialer.ControlContext(context.Background(), "tcp", "10.0.0.7:3128", nil); !errors.Is(err, ErrSSRFBlocked) {
			t.Errorf("ControlContext(10.0.0.7:3128) = %v, want ErrSSRFBlocked", err)
		}
	})
}

// TestRequestThroughAHostnameProxyReachesIt is the end-to-end form: the proxy
// URL names its endpoint by hostname ("localhost:3128"), so the string
// allowlist stores a name while the socket guard only ever sees the resolved
// dial address ("127.0.0.1:3128"). Before the resolution match that mismatch
// made every proxied dial die with
//
//	proxyconnect tcp: dial tcp <proxy-ip>:3128: request to private/reserved IP
//	blocked by SSRF protection ... (strictProxy=true)
//
// localhost resolves through the OS resolver on every platform, so this stays
// deterministic where a made-up container name would need a DNS seam the
// transport itself does not use.
func TestRequestThroughAHostnameProxyReachesIt(t *testing.T) {
	var seenPath string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	// The allowlist stores the port the operator configured, and a port the
	// dial address does not carry is the signal that an address is a *target*
	// rather than the proxy — that filter must stay. So the fake proxy has to
	// listen on exactly 127.0.0.1:3128, the address the transport dials.
	listener, err := net.Listen("tcp", "127.0.0.1:3128")
	if err != nil {
		t.Skipf("127.0.0.1:3128 is in use: %v", err)
	}
	fakeProxy := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: handler},
	}
	fakeProxy.Start()
	defer fakeProxy.Close()

	tr := NewSafeTransportForProxy("http://localhost:3128")
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	resp, err := client.Get("http://upstream.invalid/v1/models")
	if err != nil {
		if errors.Is(err, ErrSSRFBlocked) {
			t.Fatalf("request through the hostname-named proxy = %v; the operator's own proxy must not be treated as an SSRF target", err)
		}
		t.Fatalf("request through the hostname-named proxy = %v, want it to reach the proxy", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 from the fake proxy", resp.StatusCode)
	}
	if seenPath != "/v1/models" {
		t.Errorf("proxy saw path %q, want /v1/models", seenPath)
	}
}

// TestCancelledLookupDoesNotPoisonTheResolutionCache pins the concurrency
// property a shared singleflight flight has to keep: the lookup serves every
// dial through the same proxy, so one client hanging up mid-lookup must not
// cancel it for the others or cache the cancellation as a failure — that
// would refuse every proxied dial for the failure TTL and re-introduce the
// symptom from an unrelated aborted request.
func TestCancelledLookupDoesNotPoisonTheResolutionCache(t *testing.T) {
	withProxyNameResolution(t, map[string]string{"proxy-internal": "198.19.0.1"})

	orig := lookupIPAddr
	lookupIPAddr = func(ctx context.Context, name string) ([]net.IPAddr, error) {
		// Deliberately does NOT observe ctx before sleeping: the cancellation
		// has to land mid-lookup, the way a real client hangup does.
		time.Sleep(80 * time.Millisecond)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		return []net.IPAddr{{IP: net.ParseIP("198.19.0.1")}}, nil
	}
	t.Cleanup(func() { lookupIPAddr = orig })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resolveProxyIPs(ctx, "proxy-internal")

	// A healthy caller immediately after must still get the real answer.
	got := resolveProxyIPs(context.Background(), "proxy-internal")
	if len(got) == 0 {
		t.Error("healthy caller after a cancelled lookup got no addresses; the failure cache was poisoned by a caller hangup")
	}
}
