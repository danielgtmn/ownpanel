package dns

import (
	"strings"

	"golang.org/x/net/publicsuffix"
)

// IsRegistrable reports whether name (lower case, no trailing dot) is a
// registrable domain: one label below a public suffix, like example.com or
// example.co.uk, but not www.example.com and not a public suffix itself.
func IsRegistrable(name string) bool {
	name = normalizeName(name)
	if name == "" || !strings.Contains(name, ".") {
		return false
	}
	etld1, err := publicsuffix.EffectiveTLDPlusOne(name)
	if err != nil || etld1 != name {
		return false
	}
	// Unknown top-level domains count as public suffixes under the default
	// rule; only accept names whose suffix is actually listed.
	_, icann := publicsuffix.PublicSuffix(name)
	return icann || listedPrivate(name)
}

// listedPrivate: the suffix comes from the private part of the list (like
// github.io), which publicsuffix reports as not ICANN but still listed.
func listedPrivate(name string) bool {
	suffix, _ := publicsuffix.PublicSuffix(name)
	return strings.Contains(suffix, ".")
}

// InZone reports whether domain is the zone itself or a name below it.
func InZone(domain, zone string) bool {
	d, z := normalizeName(domain), normalizeName(zone)
	return d != "" && z != "" && (d == z || strings.HasSuffix(d, "."+z))
}
