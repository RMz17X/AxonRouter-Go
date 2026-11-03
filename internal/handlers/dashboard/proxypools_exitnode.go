package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ExitNodeResult holds the probed egress IPs and their geolocational metadata.
type ExitNodeResult struct {
	IPv4    string `json:"exitIpv4,omitempty"`
	IPv6    string `json:"exitIpv6,omitempty"`
	Country string `json:"exitCountry,omitempty"`
	City    string `json:"exitCity,omitempty"`
	ISP     string `json:"exitIsp,omitempty"`
	Org     string `json:"exitOrg,omitempty"`
	ASN     string `json:"exitAsn,omitempty"`
}

// cachedExitMeta holds resolved IP metadata in memory.
type cachedExitMeta struct {
	Country string
	City    string
	ISP     string
	Org     string
	ASN     string
}

// exitNodeMetaCache prevents repeated lookup calls to ip-api/ipwhois for identical IPs.
var exitNodeMetaCache sync.Map // map[string]*cachedExitMeta

var (
	ipv4ProbeTargets = []string{
		"http://v4.ident.me",
		"http://whatismyip.akamai.com",
		"https://ipv4.icanhazip.com",
	}
	ipv6ProbeTargets = []string{
		"http://v6.ident.me",
		"https://ipv6.icanhazip.com",
	}
)

// probeRelayExitNodes discovers both IPv4 and IPv6 egress addresses through an edge relay.
func probeRelayExitNodes(ctx context.Context, relayURL string) ExitNodeResult {
	var res ExitNodeResult
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		res.IPv4 = probeRelayEndpoint(ctx, relayURL, ipv4ProbeTargets, false)
	}()
	go func() {
		defer wg.Done()
		res.IPv6 = probeRelayEndpoint(ctx, relayURL, ipv6ProbeTargets, true)
	}()
	wg.Wait()

	lookupIP := res.IPv4
	if lookupIP == "" {
		lookupIP = res.IPv6
	}
	if lookupIP != "" {
		meta := resolveIPMetadata(ctx, lookupIP)
		if meta != nil {
			res.Country = meta.Country
			res.City = meta.City
			res.ISP = meta.ISP
			res.Org = meta.Org
			res.ASN = meta.ASN
		}
	}
	return res
}

// probeProxyExitNodes discovers IPv4 and IPv6 egress addresses through an HTTP/SOCKS proxy.
func probeProxyExitNodes(ctx context.Context, client *http.Client) ExitNodeResult {
	var res ExitNodeResult
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		res.IPv4 = probeProxyEndpoint(ctx, client, ipv4ProbeTargets, false)
	}()
	go func() {
		defer wg.Done()
		res.IPv6 = probeProxyEndpoint(ctx, client, ipv6ProbeTargets, true)
	}()
	wg.Wait()

	lookupIP := res.IPv4
	if lookupIP == "" {
		lookupIP = res.IPv6
	}
	if lookupIP != "" {
		meta := resolveIPMetadata(ctx, lookupIP)
		if meta != nil {
			res.Country = meta.Country
			res.City = meta.City
			res.ISP = meta.ISP
			res.Org = meta.Org
			res.ASN = meta.ASN
		}
	}
	return res
}

func probeRelayEndpoint(ctx context.Context, relayURL string, targets []string, wantIPv6 bool) string {
	tr := &http.Transport{DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 4 * time.Second}

	for _, target := range targets {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, relayURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("x-relay-target", target)
		req.Header.Set("x-relay-path", "/")

		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 128))
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			continue
		}
		ipStr := strings.TrimSpace(string(body))
		parsed := net.ParseIP(ipStr)
		if parsed == nil {
			continue
		}
		if wantIPv6 && parsed.To4() == nil && parsed.To16() != nil {
			return ipStr
		}
		if !wantIPv6 && parsed.To4() != nil {
			return ipStr
		}
	}
	return ""
}

func probeProxyEndpoint(ctx context.Context, client *http.Client, targets []string, wantIPv6 bool) string {
	for _, target := range targets {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 128))
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			continue
		}
		ipStr := strings.TrimSpace(string(body))
		parsed := net.ParseIP(ipStr)
		if parsed == nil {
			continue
		}
		if wantIPv6 && parsed.To4() == nil && parsed.To16() != nil {
			return ipStr
		}
		if !wantIPv6 && parsed.To4() != nil {
			return ipStr
		}
	}
	return ""
}

// resolveIPMetadata performs an out-of-band lookup of the IP to discover ISP and geo details.
// It checks in-memory cache first to strictly guard against third-party API rate limits.
func resolveIPMetadata(ctx context.Context, ip string) *cachedExitMeta {
	if ip == "" {
		return nil
	}
	if val, ok := exitNodeMetaCache.Load(ip); ok {
		if meta, ok := val.(*cachedExitMeta); ok {
			return meta
		}
	}

	meta := fetchIPMeta(ctx, ip)
	if meta != nil {
		exitNodeMetaCache.Store(ip, meta)
	}
	return meta
}

func fetchIPMeta(ctx context.Context, ip string) *cachedExitMeta {
	client := &http.Client{Timeout: 3 * time.Second}

	// 1. Primary: ip-api.com
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://ip-api.com/json/"+ip+"?fields=status,country,city,isp,org,as", nil)
	if err == nil {
		resp, err := client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			var data struct {
				Status  string `json:"status"`
				Country string `json:"country"`
				City    string `json:"city"`
				ISP     string `json:"isp"`
				Org     string `json:"org"`
				AS      string `json:"as"`
			}
			if json.NewDecoder(resp.Body).Decode(&data) == nil && data.Status == "success" {
				asn := data.AS
				if idx := strings.Index(asn, " "); idx != -1 {
					asn = asn[:idx]
				}
				return &cachedExitMeta{
					Country: data.Country,
					City:    data.City,
					ISP:     data.ISP,
					Org:     data.Org,
					ASN:     asn,
				}
			}
		}
	}

	// 2. Secondary fallback: ipwho.is
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://ipwho.is/"+ip, nil)
	if err == nil {
		resp2, err := client.Do(req2)
		if err == nil {
			defer resp2.Body.Close()
			var data2 struct {
				Success    bool   `json:"success"`
				Country    string `json:"country"`
				City       string `json:"city"`
				Connection struct {
					ASN int    `json:"asn"`
					Org string `json:"org"`
					ISP string `json:"isp"`
				} `json:"connection"`
			}
			if json.NewDecoder(resp2.Body).Decode(&data2) == nil && data2.Success {
				asn := ""
				if data2.Connection.ASN > 0 {
					asn = fmt.Sprintf("AS%d", data2.Connection.ASN)
				}
				return &cachedExitMeta{
					Country: data2.Country,
					City:    data2.City,
					ISP:     data2.Connection.ISP,
					Org:     data2.Connection.Org,
					ASN:     asn,
				}
			}
		}
	}

	return nil
}
