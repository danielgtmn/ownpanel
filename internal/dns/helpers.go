package dns

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sort"
	"strings"
)

func normalizeName(s string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
}

// FindZone returns the zone with the longest name that is the domain itself or
// a parent of it, and the record name relative to that zone ("@" for the
// apex, "www", "a.b"). Matching is case-insensitive and tolerates a trailing
// dot. "example.com" does not match a zone "ample.com".
func FindZone(zones []Zone, domain string) (zone Zone, name string, ok bool) {
	d := normalizeName(domain)
	if d == "" {
		return Zone{}, "", false
	}
	best := -1
	for i, z := range zones {
		zn := normalizeName(z.Name)
		if zn == "" || len(zn) <= best {
			continue
		}
		if d == zn || strings.HasSuffix(d, "."+zn) {
			best = len(zn)
			zone = zones[i]
			if d == zn {
				name = "@"
			} else {
				name = strings.TrimSuffix(d, "."+zn)
			}
			ok = true
		}
	}
	return zone, name, ok
}

// State is the result of checking where a domain points.
type State int

const (
	PointsHere      State = iota // every resolved address is one of the server's
	PointsElsewhere              // at least one resolved address is foreign
	NoRecord                     // the domain has no address records
	LookupFailed                 // the lookup failed for another reason
)

func (s State) String() string {
	switch s {
	case PointsHere:
		return "points-here"
	case PointsElsewhere:
		return "points-elsewhere"
	case NoRecord:
		return "no-record"
	case LookupFailed:
		return "lookup-failed"
	}
	return "unknown"
}

// Check is the outcome of CheckDomain.
type Check struct {
	Domain string
	State  State
	// Found lists the foreign addresses for PointsElsewhere, and all resolved
	// addresses for PointsHere. It is empty for NoRecord and LookupFailed.
	Found []string
}

// Resolver is satisfied by *net.Resolver.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// CheckDomain resolves domain (A and AAAA) and compares it with serverIPs.
//
// Rule (this is what the UI explains to the user):
//   - PointsHere: at least one address was resolved and every resolved
//     address is one of serverIPs.
//   - PointsElsewhere: at least one address was resolved and at least one of
//     them is not one of serverIPs; the foreign ones are reported in Found.
//   - NoRecord: the name does not exist or has no addresses.
//   - LookupFailed: any other lookup error (timeout, server failure, ...).
func CheckDomain(ctx context.Context, r Resolver, domain string, serverIPs []netip.Addr) Check {
	c := Check{Domain: domain}
	host := normalizeName(domain)
	addrs, err := r.LookupNetIP(ctx, "ip", host)
	if err != nil {
		var de *net.DNSError
		if errors.As(err, &de) && de.IsNotFound {
			c.State = NoRecord
			return c
		}
		c.State = LookupFailed
		return c
	}
	server := make(map[netip.Addr]bool, len(serverIPs))
	for _, ip := range serverIPs {
		server[ip.Unmap()] = true
	}
	seen := map[netip.Addr]bool{}
	var all, foreign []string
	for _, a := range addrs {
		a = a.Unmap().WithZone("")
		if seen[a] {
			continue
		}
		seen[a] = true
		all = append(all, a.String())
		if !server[a] {
			foreign = append(foreign, a.String())
		}
	}
	sort.Strings(all)
	sort.Strings(foreign)
	switch {
	case len(all) == 0:
		c.State = NoRecord
	case len(foreign) > 0:
		c.State = PointsElsewhere
		c.Found = foreign
	default:
		c.State = PointsHere
		c.Found = all
	}
	return c
}

// AddressRecords returns the A and/or AAAA record sets that point name at the
// server: one set per address family that has addresses, A before AAAA,
// addresses sorted and de-duplicated.
func AddressRecords(name string, serverIPs []netip.Addr, ttl int) []RecordSet {
	var v4, v6 []string
	seen := map[netip.Addr]bool{}
	for _, ip := range serverIPs {
		ip = ip.Unmap().WithZone("")
		if !ip.IsValid() || seen[ip] {
			continue
		}
		seen[ip] = true
		if ip.Is4() {
			v4 = append(v4, ip.String())
		} else {
			v6 = append(v6, ip.String())
		}
	}
	sort.Strings(v4)
	sort.Strings(v6)
	var out []RecordSet
	if len(v4) > 0 {
		out = append(out, RecordSet{Name: name, Type: "A", TTL: ttl, Values: v4})
	}
	if len(v6) > 0 {
		out = append(out, RecordSet{Name: name, Type: "AAAA", TTL: ttl, Values: v6})
	}
	return out
}
