package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeRelayExitNodes_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := r.Header.Get("x-relay-target")
		if target == "http://v4.ident.me" || target == "https://ipv4.icanhazip.com" || target == "http://whatismyip.akamai.com" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("172.69.166.14\n"))
			return
		}
		if target == "http://v6.ident.me" || target == "https://ipv6.icanhazip.com" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("2a06:98c0:3600::103\n"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	// Pre-seed cache to avoid network calls during test
	exitNodeMetaCache.Store("172.69.166.14", &cachedExitMeta{
		Country: "Singapore",
		City:    "Singapore",
		ISP:     "Cloudflare, Inc.",
		Org:     "Cloudflare WARP",
		ASN:     "AS13335",
	})

	res := probeRelayExitNodes(context.Background(), server.URL)
	if res.IPv4 != "172.69.166.14" {
		t.Errorf("expected IPv4 172.69.166.14, got %q", res.IPv4)
	}
	if res.IPv6 != "2a06:98c0:3600::103" {
		t.Errorf("expected IPv6 2a06:98c0:3600::103, got %q", res.IPv6)
	}
	if res.Country != "Singapore" || res.City != "Singapore" || res.ASN != "AS13335" {
		t.Errorf("unexpected metadata: %+v", res)
	}
}

func TestResolveIPMetadata_Cache(t *testing.T) {
	ip := "1.2.3.4"
	meta := &cachedExitMeta{
		Country: "TestCountry",
		City:    "TestCity",
		ISP:     "TestISP",
		Org:     "TestOrg",
		ASN:     "AS12345",
	}
	exitNodeMetaCache.Store(ip, meta)

	got := resolveIPMetadata(context.Background(), ip)
	if got == nil || got.Country != "TestCountry" || got.ASN != "AS12345" {
		t.Errorf("expected cached meta, got %+v", got)
	}
}
