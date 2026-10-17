package web

// Client IP resolution, the algorithm of gin's Context.ClientIP with one configured header:
// the header is believed only when the direct peer is a trusted proxy (Router.SetTrustedProxies),
// and it is read right to left, skipping trusted hops.

import (
	"net"
	"strings"
)

// RemoteIP returns the host part of Request.RemoteAddr, "" when it has no port or does not parse.
func (c *Context) RemoteIP() string {
	ip, _, err := net.SplitHostPort(strings.TrimSpace(c.Request.RemoteAddr))
	if err != nil {
		return ""
	}
	return ip
}

// ClientIP returns the client address: "" when the peer address does not parse; the address from
// the router's client IP header when the peer is a trusted proxy and the header is valid;
// otherwise the peer. Without a router (NewTestContext) it is the peer.
func (c *Context) ClientIP() string {
	peer := net.ParseIP(c.RemoteIP())
	if peer == nil {
		return ""
	}
	if c.router == nil {
		return peer.String()
	}
	proxies, header := c.router.trust()
	if header != "" && httpxInNets(peer, proxies) {
		if ip, ok := httpxForwardedIP(c.Request.Header.Get(header), proxies); ok {
			return ip
		}
	}
	return peer.String()
}

// httpxForwardedIP walks a forwarded header right to left and returns the first address that is
// not a trusted proxy, or the left-most one. An entry that is not an IP stops the walk: the header
// is then not used.
func httpxForwardedIP(header string, proxies []*net.IPNet) (string, bool) {
	if header == "" {
		return "", false
	}
	items := strings.Split(header, ",")
	for i := len(items) - 1; i >= 0; i-- {
		ipStr := strings.TrimSpace(items[i])
		ip := net.ParseIP(ipStr)
		if ip == nil {
			break
		}
		if i == 0 || !httpxInNets(ip, proxies) {
			return ipStr, true
		}
	}
	return "", false
}

// httpxInNets reports whether ip is inside one of nets.
func httpxInNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
