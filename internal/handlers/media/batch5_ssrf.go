package media

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// SSRF layer-1 guard + local-request detection for the Cowork MCP
// tools probe. Ported from Axon's src/shared/utils/ssrfGuard.js
// (assertPublicUrl — the literal host checks only; the route does not
// use the DNS-resolving layer) and isLocalRequest from
// src/dashboardGuard.js.

// --- isLocalRequest -------------------------------------------------

// isLocalRequest reports whether the request arrived directly from a
// loopback peer without a proxy hop: a stamped via-proxy header
// disqualifies it, the socket peer must be loopback, and a present
// Origin must name a loopback host.
func isLocalRequest(r *http.Request) bool {
	if r.Header.Get("X-Axonrouter-Via-Proxy") != "" {
		return false
	}
	if !isLoopbackPeer(r) {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !isLoopbackHostname(u.Hostname()) {
			return false
		}
	}
	return true
}

func isLoopbackPeer(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isLoopbackHostname(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// --- assertPublicUrl (layer 1) --------------------------------------

var blockedHostnames = map[string]bool{
	"localhost":     true,
	"ip6-localhost": true,
	"ip6-loopback":  true,
}

var blockedSuffixes = []string{".internal", ".local", ".localhost"}

type v4Range struct {
	base uint32
	bits uint
}

func ipv4ToInt(host string) (uint32, bool) {
	parts := strings.Split(host, ".")
	if len(parts) != 4 {
		return 0, false
	}
	var value uint32
	for _, part := range parts {
		if len(part) == 0 || len(part) > 3 {
			return 0, false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return 0, false
			}
		}
		octet, err := strconv.Atoi(part)
		if err != nil || octet > 255 {
			return 0, false
		}
		value = value*256 + uint32(octet)
	}
	return value, true
}

var blockedV4Ranges = []v4Range{
	{mustV4("0.0.0.0"), 8},
	{mustV4("10.0.0.0"), 8},
	{mustV4("100.64.0.0"), 10}, // CGNAT — also used by some cloud metadata proxies
	{mustV4("127.0.0.0"), 8},
	{mustV4("169.254.0.0"), 16}, // includes 169.254.169.254 cloud metadata
	{mustV4("172.16.0.0"), 12},
	{mustV4("192.168.0.0"), 16},
}

func mustV4(s string) uint32 {
	v, _ := ipv4ToInt(s)
	return v
}

func isBlockedIPv4Int(ip uint32) bool {
	for _, r := range blockedV4Ranges {
		var mask uint32
		if r.bits != 0 {
			mask = (0xFFFFFFFF << (32 - r.bits))
		}
		if ip&mask == r.base&mask {
			return true
		}
	}
	return false
}

func isBlockedIPv4(host string) bool {
	ip, ok := ipv4ToInt(host)
	if !ok {
		return false
	}
	return isBlockedIPv4Int(ip)
}

var (
	v4TailRE    = regexp.MustCompile(`(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})$`)
	hextetRE    = regexp.MustCompile(`^[0-9a-f]{1,4}$`)
	trailingDot = regexp.MustCompile(`\.+$`)
)

// parseIPv6ToGroups parses any textual IPv6 representation (embedded
// dotted-IPv4 tail, "::" compression anywhere) into 8 groups; nil
// when the string is not a valid IPv6 literal.
func parseIPv6ToGroups(rawHost string) []uint16 {
	host := strings.ToLower(rawHost)
	var v4Groups []uint16
	if m := v4TailRE.FindString(host); m != "" {
		v4Int, ok := ipv4ToInt(m)
		if !ok {
			return nil
		}
		v4Groups = []uint16{uint16((v4Int >> 16) & 0xFFFF), uint16(v4Int & 0xFFFF)}
		host = host[:len(host)-len(m)]
		if strings.HasSuffix(host, "::") {
			// "::" compression marker itself — leave both colons.
		} else if strings.HasSuffix(host, ":") {
			host = host[:len(host)-1]
		}
	}
	parts := strings.Split(host, "::")
	if len(parts) > 2 {
		return nil
	}
	var groups []uint16
	if len(parts) == 2 {
		head, okH := parseHextets(parts[0])
		tail, okT := parseHextets(parts[1])
		if !okH || !okT {
			return nil
		}
		missing := 8 - len(head) - len(tail) - len(v4Groups)
		if missing < 0 {
			return nil
		}
		groups = append(groups, head...)
		for i := 0; i < missing; i++ {
			groups = append(groups, 0)
		}
		groups = append(groups, tail...)
		groups = append(groups, v4Groups...)
	} else {
		all, ok := parseHextets(host)
		if !ok {
			return nil
		}
		groups = append(all, v4Groups...)
	}
	if len(groups) != 8 {
		return nil
	}
	return groups
}

func parseHextets(s string) ([]uint16, bool) {
	if s == "" {
		return []uint16{}, true
	}
	segs := strings.Split(s, ":")
	out := make([]uint16, 0, len(segs))
	for _, seg := range segs {
		if !hextetRE.MatchString(seg) {
			return nil, false
		}
		n, err := strconv.ParseUint(seg, 16, 32)
		if err != nil {
			return nil, false
		}
		out = append(out, uint16(n))
	}
	return out, true
}

func isBlockedIPv6Groups(g []uint16) bool {
	if len(g) != 8 {
		return false
	}
	isZero := func(n int) bool { return g[n] == 0 }
	// loopback ::1
	loopback := true
	for i := 0; i < 7; i++ {
		if !isZero(i) {
			loopback = false
		}
	}
	if loopback && g[7] == 1 {
		return true
	}
	// unspecified ::
	allZero := true
	for _, x := range g {
		if x != 0 {
			allZero = false
		}
	}
	if allZero {
		return true
	}
	// link-local fe80::/10
	if (g[0] & 0xFFC0) == 0xFE80 {
		return true
	}
	// unique local fc00::/7
	if (g[0] & 0xFE00) == 0xFC00 {
		return true
	}
	low32 := (uint32(g[6]) << 16) | uint32(g[7])
	head5Zero := isZero(0) && isZero(1) && isZero(2) && isZero(3) && isZero(4)
	// IPv4-mapped ::ffff:0:0/96
	if head5Zero && g[5] == 0xFFFF {
		return isBlockedIPv4Int(low32)
	}
	// NAT64 well-known prefix 64:ff9b::/96
	if g[0] == 0x0064 && g[1] == 0xFF9B && isZero(2) && isZero(3) && isZero(4) && isZero(5) {
		return isBlockedIPv4Int(low32)
	}
	// IPv4-compatible ::a.b.c.d/96 (excludes :: and ::1, matched above)
	if head5Zero && isZero(5) && low32 != 0 && low32 != 1 {
		return isBlockedIPv4Int(low32)
	}
	return false
}

func normalizeHost(hostname string) string {
	return trailingDot.ReplaceAllString(strings.ToLower(hostname), "")
}

func isBlockedHost(host string) bool {
	if blockedHostnames[host] {
		return true
	}
	for _, s := range blockedSuffixes {
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	if isBlockedIPv4(host) {
		return true
	}
	if strings.Contains(host, ":") {
		trimmed := strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		if groups := parseIPv6ToGroups(trimmed); groups != nil && isBlockedIPv6Groups(groups) {
			return true
		}
	}
	return false
}

// assertPublicURL throws (returns an error) when the URL targets a
// non-public host by literal hostname/IP alone — layer 1 of Axon's
// guard. The caller maps the error to a 400.
func assertPublicURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return errors.New("Blocked URL: internal host")
	}
	if isBlockedHost(normalizeHost(parsed.Hostname())) {
		return errors.New("Blocked URL: internal host")
	}
	return nil
}
