package panel

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// nonPublic are ranges that IsGlobalUnicast accepts but that are never
// reachable from the Internet.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), // documentation
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("100::/64"),      // discard
	netip.MustParsePrefix("2001:db8::/32"), // documentation
	netip.MustParsePrefix("fec0::/10"),     // former site-local
}

func isPublicUnicast(ip netip.Addr) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// ParseServerIPs parses the values of -server-ip; each value may hold
// several addresses separated by commas. Unless allowNonPublic is set (for
// local testing without public certificates), every address must be a
// public unicast address. The result is sorted (IPv4 first) and has no
// duplicates.
func ParseServerIPs(values []string, allowNonPublic bool) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			ip, err := netip.ParseAddr(part)
			if err != nil || ip.Zone() != "" {
				return nil, fmt.Errorf("%q is not an IPv4 or IPv6 address", part)
			}
			ip = ip.Unmap()
			if ip.IsUnspecified() || ip.IsMulticast() {
				return nil, fmt.Errorf("%s cannot be the address of a server", ip)
			}
			if !allowNonPublic && !isPublicUnicast(ip) {
				return nil, fmt.Errorf("%s is not a public address; domains must point at an address that is reachable from the Internet", ip)
			}
			if !slices.Contains(out, ip) {
				out = append(out, ip)
			}
		}
	}
	slices.SortFunc(out, func(a, b netip.Addr) int { return a.Compare(b) })
	return out, nil
}

// ServerIPs returns the server's public addresses as configured with
// -server-ip; empty if none are.
func (s *Service) ServerIPs(ctx context.Context, a Actor) ([]string, error) {
	if err := needAuth(a); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(s.serverIPs))
	for _, ip := range s.serverIPs {
		out = append(out, ip.String())
	}
	return out, nil
}

func (s *Service) needServerIPs() error {
	if len(s.serverIPs) == 0 {
		return errorf(Conflict, "The server's public address is not configured (start ownpaneld with -server-ip)")
	}
	return nil
}
