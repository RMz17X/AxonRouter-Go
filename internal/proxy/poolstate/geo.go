package poolstate

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	json "encoding/json/v2"

	"golang.org/x/net/proxy"
)

// Geo is one pool's probed egress identity (upstream poolGeo shape).
type Geo struct {
	IP           string `json:"ip"`
	Country      string `json:"country"`
	Region       string `json:"region"`
	City         string `json:"city"`
	Org          string `json:"org"`
	IsDatacenter bool   `json:"isDatacenter"`
}

type ipHistoryEntry struct {
	IP string `json:"ip"`
	TS int64  `json:"ts"`
}

type geoEntry struct {
	Geo
	TS        int64            `json:"ts"`
	IPHistory []ipHistoryEntry `json:"ipHistory"`
}

const (
	geoTTL         = time.Hour
	geoHistoryMax  = 8
	datacenterOrgs = "cloudflare|vercel|amazon|aws|google|microsoft|azure|digitalocean|hetzner|ovh|contabo|leaseweb"
)

var (
	geoMu    sync.Mutex
	geoCache = map[string]*geoEntry{}
)

// GetPoolGeo returns the cached egress for a pool in the API-facing shape
// ({...geo, ts, ipHistory, ipCount, isUnstable}), or nil when unknown or
// past the TTL.
func GetPoolGeo(poolID string) map[string]any {
	geoMu.Lock()
	defer geoMu.Unlock()
	entry, ok := geoCache[poolID]
	if !ok {
		return nil
	}
	if entry.TS+geoTTL.Milliseconds() < time.Now().UnixMilli() {
		delete(geoCache, poolID)
		return nil
	}
	return entry.toMap()
}

func (e *geoEntry) toMap() map[string]any {
	ips := map[string]bool{}
	if e.IP != "" {
		ips[e.IP] = true
	}
	history := make([]any, 0, len(e.IPHistory))
	for _, h := range e.IPHistory {
		if h.IP != "" {
			ips[h.IP] = true
		}
		history = append(history, map[string]any{"ip": h.IP, "ts": h.TS})
	}
	return map[string]any{
		"ip":           e.IP,
		"country":      e.Country,
		"region":       e.Region,
		"city":         e.City,
		"org":          e.Org,
		"isDatacenter": e.IsDatacenter,
		"ts":           e.TS,
		"ipHistory":    history,
		"ipCount":      len(ips),
		"isUnstable":   len(ips) >= 2,
	}
}

// SetPoolGeo caches a fresh probe result, remembering the egress IP we are
// leaving so flapping relays classify as unstable.
func SetPoolGeo(poolID string, geo Geo) {
	if poolID == "" || geo.IP == "" {
		return
	}
	geoMu.Lock()
	defer geoMu.Unlock()
	var history []ipHistoryEntry
	if prev, ok := geoCache[poolID]; ok {
		history = prev.IPHistory
		if prev.IP != "" && prev.IP != geo.IP {
			history = append(history, ipHistoryEntry{IP: prev.IP, TS: time.Now().UnixMilli()})
			if len(history) > geoHistoryMax {
				history = history[len(history)-geoHistoryMax:]
			}
		}
	}
	geoCache[poolID] = &geoEntry{Geo: geo, TS: time.Now().UnixMilli(), IPHistory: history}
}

// ProbeFailure is a classified geo probe failure (upstream error vocabulary:
// rate-limit | server | no-ip | network | timeout).
type ProbeFailure struct {
	Kind   string
	Detail string
}

func (e *ProbeFailure) Error() string { return e.Kind + ": " + e.Detail }

type geoProbeSource struct {
	name  string
	url   string
	parse func(map[string]any) Geo
}

func geoProbes() []geoProbeSource {
	if custom := os.Getenv("GEO_PROBE_URL"); custom != "" {
		return []geoProbeSource{{name: "", url: custom, parse: parseCustomGeo}}
	}
	return []geoProbeSource{
		{"ipwho.is", "https://ipwho.is/", func(d map[string]any) Geo {
			org := getStr(d, "org")
			if org == "" {
				if conn, ok := d["connection"].(map[string]any); ok {
					org = getStr(conn, "org")
				}
			}
			return Geo{IP: getStr(d, "ip"), Country: getStr(d, "country"), Region: getStr(d, "region"), City: getStr(d, "city"), Org: org}
		}},
		{"ip-api", "https://ip-api.com/json/?fields=status,message,query,country,regionName,city,org", func(d map[string]any) Geo {
			return Geo{IP: getStr(d, "query"), Country: getStr(d, "country"), Region: getStr(d, "regionName"), City: getStr(d, "city"), Org: getStr(d, "org")}
		}},
		{"ipapi.co", "https://ipapi.co/json/", func(d map[string]any) Geo {
			return Geo{IP: getStr(d, "ip"), Country: getStr(d, "country_name"), Region: getStr(d, "region"), City: getStr(d, "city"), Org: getStr(d, "org")}
		}},
		{"ipinfo", "https://ipinfo.io/json", func(d map[string]any) Geo {
			return Geo{IP: getStr(d, "ip"), Country: getStr(d, "country"), Region: getStr(d, "region"), City: getStr(d, "city"), Org: getStr(d, "org")}
		}},
	}
}

func parseCustomGeo(d map[string]any) Geo {
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v := getStr(d, k); v != "" {
				return v
			}
		}
		return ""
	}
	org := pick("org", "organization", "isp", "orgName", "asn")
	if org == "" {
		if conn, ok := d["connection"].(map[string]any); ok {
			org = getStr(conn, "org")
		}
	}
	return Geo{
		IP:      pick("query", "ip"),
		Country: pick("country", "country_name", "countryName"),
		Region:  pick("regionName", "region", "state", "regionCode"),
		City:    pick("city"),
		Org:     org,
	}
}

func getStr(d map[string]any, key string) string {
	if d == nil {
		return ""
	}
	if s, ok := d[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func isDatacenterOrg(org string) bool {
	lower := strings.ToLower(org)
	for _, token := range strings.Split(datacenterOrgs, "|") {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

// ProxyClient builds an HTTP client that egresses through the pool: relay
// pools are plain HTTPS endpoints addressed with x-relay-* headers, so for
// them the client is direct and the caller adds the headers.
func ProxyClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{DisableKeepAlives: true}
	switch strings.ToLower(parsed.Scheme) {
	case "socks5", "socks5h", "socks4", "socks4a":
		var auth *proxy.Auth
		if parsed.User != nil {
			pass, _ := parsed.User.Password()
			auth = &proxy.Auth{User: parsed.User.Username(), Password: pass}
		}
		forward := &net.Dialer{Timeout: timeout}
		dialer, err := proxy.SOCKS5("tcp", parsed.Host, auth, forward)
		if err != nil {
			return nil, err
		}
		if cd, ok := dialer.(proxy.ContextDialer); ok {
			transport.DialContext = cd.DialContext
		} else {
			transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			}
		}
	default:
		transport.Proxy = http.ProxyURL(parsed)
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

func isRelayType(poolType string) bool {
	switch poolType {
	case "vercel", "cloudflare", "deno":
		return true
	}
	return false
}

// ProbePoolGeo probes the pool's egress through the geo endpoint chain,
// returning the first successful identity. Fail-open like upstream: the
// failure carries the most specific classification seen.
func ProbePoolGeo(ctx context.Context, proxyURL, poolType string, timeout time.Duration) (*Geo, *ProbeFailure) {
	if proxyURL == "" {
		return nil, &ProbeFailure{Kind: "network", Detail: "no proxy url"}
	}
	last := &ProbeFailure{Kind: "network", Detail: "no provider responded"}
	for _, source := range geoProbes() {
		geo, failure := probeGeoSource(ctx, source, proxyURL, poolType, timeout)
		if failure == nil {
			return geo, nil
		}
		if failure.Kind == "rate-limit" || failure.Kind == "server" {
			last = failure
		} else if last.Kind == "network" {
			last = failure
		}
	}
	return nil, last
}

func probeGeoSource(ctx context.Context, source geoProbeSource, proxyURL, poolType string, timeout time.Duration) (*Geo, *ProbeFailure) {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var client *http.Client
	var req *http.Request
	var err error
	if isRelayType(poolType) {
		client = &http.Client{Timeout: timeout}
		req, err = http.NewRequestWithContext(probeCtx, http.MethodGet, proxyURL, nil)
		if err != nil {
			return nil, &ProbeFailure{Kind: "network", Detail: err.Error()}
		}
		target, _ := url.Parse(source.url)
		req.Header.Set("x-relay-target", target.Scheme+"://"+target.Host)
		req.Header.Set("x-relay-path", target.RequestURI())
	} else {
		client, err = ProxyClient(proxyURL, timeout)
		if err != nil {
			return nil, &ProbeFailure{Kind: "network", Detail: err.Error()}
		}
		req, err = http.NewRequestWithContext(probeCtx, http.MethodGet, source.url, nil)
		if err != nil {
			return nil, &ProbeFailure{Kind: "network", Detail: err.Error()}
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		kind := "network"
		if probeCtx.Err() == context.DeadlineExceeded {
			kind = "timeout"
		}
		return nil, &ProbeFailure{Kind: kind, Detail: err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		kind := "network"
		if resp.StatusCode == 429 || resp.StatusCode == 403 {
			kind = "rate-limit"
		} else if resp.StatusCode >= 500 {
			kind = "server"
		}
		detail := string(body)
		if len(detail) > 80 {
			detail = detail[:80]
		}
		return nil, &ProbeFailure{Kind: kind, Detail: fmt.Sprintf("%d %s", resp.StatusCode, detail)}
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil || data == nil {
		name := source.name
		if name == "" {
			name = "custom"
		}
		return nil, &ProbeFailure{Kind: "no-ip", Detail: name}
	}
	geo := source.parse(data)
	if strings.TrimSpace(geo.IP) == "" {
		name := source.name
		if name == "" {
			name = "custom"
		}
		return nil, &ProbeFailure{Kind: "no-ip", Detail: name}
	}
	geo.IP = strings.TrimSpace(geo.IP)
	geo.Country = strings.TrimSpace(geo.Country)
	geo.Region = strings.TrimSpace(geo.Region)
	geo.City = strings.TrimSpace(geo.City)
	geo.Org = strings.TrimSpace(geo.Org)
	geo.IsDatacenter = isDatacenterOrg(geo.Org)
	return &geo, nil
}
