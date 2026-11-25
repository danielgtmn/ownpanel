// Package netcheck decides which addresses the mail service may connect
// to on behalf of others (delivery to MX hosts, IMAP import), so nobody can
// make it reach loopback, private networks or other special ranges around
// the server it runs on.
package netcheck

import (
	"net"
	"net/netip"
)

// Special-purpose ranges netip does not classify (RFC 6890).
var special = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // this network
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, broadcast
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
}

// Public tells whether ip is a public unicast address the service may
// connect to. It is stricter than filter.Checkable (which only decides
// whether to ask blocklists about a client): loopback, private (RFC 1918,
// ULA), link-local, unspecified, multicast and the special ranges
// above are not.
func Public(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if !a.IsValid() || !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() ||
		a.IsLinkLocalUnicast() || a.IsUnspecified() || a.IsMulticast() {
		return false
	}
	for _, p := range special {
		if p.Contains(a) {
			return false
		}
	}
	// NAT64: a translator connects to the IPv4 address embedded in the
	// IPv6 one, which must be public too.
	return nat64Public(a)
}

var (
	nat64WellKnown = netip.MustParsePrefix("64:ff9b::/96")   // RFC 6052
	nat64Local     = netip.MustParsePrefix("64:ff9b:1::/48") // RFC 8215
)

// nat64Public tells whether a NAT64 address embeds a public IPv4 address
// (true for every other address). The well-known prefix is a /96. The
// local-use /48 may be used with any prefix length of RFC 6052, which this
// server cannot know; only the common /96 form (the 48 bits after the
// prefix zero) is accepted and every other one refused, rather than
// guessing where a translator would connect.
func nat64Public(a netip.Addr) bool {
	b := a.As16()
	switch {
	case nat64WellKnown.Contains(a):
	case nat64Local.Contains(a):
		for _, x := range b[6:12] {
			if x != 0 {
				return false
			}
		}
	default:
		return true
	}
	return Public(net.IP(b[12:16]))
}
