package auth

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIPResolver extracts the real client IP, trusting forwarding headers
// only when the TCP peer is a configured proxy.
type ClientIPResolver struct {
	trusted []netip.Prefix
}

// NewClientIPResolver parses a comma-separated list of IPs or CIDRs.
// An empty list trusts no proxy, so RemoteAddr is always used.
func NewClientIPResolver(trustedProxies string) (*ClientIPResolver, error) {
	r := &ClientIPResolver{}
	for _, raw := range strings.Split(trustedProxies, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, "/") {
			addr, err := netip.ParseAddr(raw)
			if err != nil {
				return nil, fmt.Errorf("invalid trusted proxy %q", raw)
			}
			r.trusted = append(r.trusted, netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()))
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy %q", raw)
		}
		r.trusted = append(r.trusted, prefix.Masked())
	}
	return r, nil
}

func (r *ClientIPResolver) isTrusted(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range r.trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// ClientIP returns the client address for rate limiting and audit hashing.
// Behind a trusted proxy it is the rightmost X-Forwarded-For entry that is not
// itself a trusted proxy. CF-Connecting-IP is ignored: a proxy other than
// Cloudflare passes a client-sent copy straight through, while Cloudflare also
// appends the client to X-Forwarded-For.
func (r *ClientIPResolver) ClientIP(req *http.Request) string {
	remote := req.RemoteAddr
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	if !r.isTrusted(remote) {
		return remote
	}

	hops := strings.Split(strings.Join(req.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop := strings.TrimSpace(hops[i])
		if _, err := netip.ParseAddr(hop); err != nil {
			break
		}
		if !r.isTrusted(hop) {
			return hop
		}
	}
	return remote
}

// RateKey groups an IPv6 client by its /64, the block one subscriber usually
// controls, so rotating addresses within it does not reset rate limits.
func RateKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil || !addr.Unmap().Is6() {
		return ip
	}
	return netip.PrefixFrom(addr, 64).Masked().String()
}
