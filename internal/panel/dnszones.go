package panel

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/danielgtmn/ownpanel/internal/dns"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// Creating and deleting whole domains (zones) at the DNS provider, through
// one's own connections. Deleting is refused while anything of any person
// (a site, a redirect, the panel itself) uses a domain in the zone.

// needZoneManage: own connections, signed in or with a token that may
// write to all sites.
func needZoneManage(a Actor) error {
	if err := needDNSManage(a); err != nil {
		return err
	}
	if a.ReadOnly {
		return errorf(Forbidden, "managing domains at the DNS provider needs a password sign-in or an access token with write access to all sites")
	}
	return nil
}

// newZoneName normalizes the name of a zone to create: a registrable domain.
func newZoneName(name string) (string, error) {
	raw := strings.TrimSpace(name)
	if raw == "" {
		return "", errorf(Invalid, "enter the domain, like example.com")
	}
	if _, err := netip.ParseAddr(strings.Trim(raw, "[]")); err == nil {
		return "", errorf(Invalid, "enter a domain like example.com, not an IP address")
	}
	if hasNonASCII(raw) {
		return "", errorf(Invalid, "the domain %q contains non-ASCII characters; use the punycode form (xn--...)", raw)
	}
	d, err := store.NormalizeDomain(raw)
	if err != nil {
		return "", errorf(Invalid, "%q is not a valid domain; enter one like example.com", raw)
	}
	if !dns.IsRegistrable(d) {
		return "", errorf(Invalid, "%s is not a domain you can register, like example.com; a subdomain such as www.example.com belongs to the zone of its domain", d)
	}
	return d, nil
}

// CreateDNSZone creates a zone for a domain in one of the actor's own
// connections and returns it with the nameservers to set at the registrar.
func (s *Service) CreateDNSZone(ctx context.Context, a Actor, connID int64, name string) (DNSZone, error) {
	if err := needZoneManage(a); err != nil {
		return DNSZone{}, err
	}
	c, err := s.usableConnection(ctx, a, connID)
	if err != nil {
		return DNSZone{}, err
	}
	d, err := newZoneName(name)
	if err != nil {
		return DNSZone{}, err
	}
	zones, err := s.zonesOf(ctx, c)
	if err != nil {
		return DNSZone{}, err
	}
	if slices.ContainsFunc(zones, func(z dns.Zone) bool { return z.Name == d }) {
		return DNSZone{}, errorf(Conflict, "the DNS connection %q already has the domain %s", c.Name, d)
	}
	p, token, err := s.provider(c)
	if err != nil {
		return DNSZone{}, err
	}
	z, err := p.CreateZone(ctx, d)
	s.dnsCache.dropZones(c.ID)
	if err != nil {
		derr := dnsError(c.Provider, c.Name, token, err)
		if errors.Is(err, dns.ErrExists) {
			derr = &Error{Kind: Conflict, Err: err, Msg: fmt.Sprintf("the domain %s already exists at %s, possibly in another project or account; remove it there first", d, providerLabel(c.Provider))}
		}
		s.noteWrite(ctx, c, err, derr)
		return DNSZone{}, derr
	}
	s.noteWrite(ctx, c, nil, nil)
	s.dnsCache.dropAllChecks()
	s.record(ctx, a, "dns.zone.create", z.Name, fmt.Sprintf("connection %q, nameservers %s", c.Name, strings.Join(z.Nameservers, ", ")))
	return zoneView(c, z), nil
}

// zoneUsers lists what uses a domain inside zone, across every person:
// sites, redirects and the panel itself. Things the actor may not see are
// described without their names.
func (s *Service) zoneUsers(ctx context.Context, a Actor, zone string) ([]string, error) {
	var uses []string
	sites, err := s.store.Sites(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	byName := map[string]store.Site{}
	for _, site := range sites {
		byName[site.Name] = site
		var in []string
		for _, d := range site.Domains {
			if dns.InZone(d, zone) {
				in = append(in, d)
			}
		}
		if len(in) == 0 {
			continue
		}
		if a.canSee(site) {
			uses = append(uses, fmt.Sprintf("the site %s (%s)", site.Name, strings.Join(in, ", ")))
		} else {
			uses = append(uses, fmt.Sprintf("a site of another person (%s)", strings.Join(in, ", ")))
		}
	}
	redirects, err := s.store.Redirects(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	for _, r := range redirects {
		if r.SourcePath != "" || !dns.InZone(r.SourceDomain, zone) {
			continue // path redirects are on site domains, listed above
		}
		if a.canSeeRedirect(r, byName) {
			uses = append(uses, "the redirect of "+r.SourceDomain)
		} else {
			uses = append(uses, fmt.Sprintf("a redirect of another person (%s)", r.SourceDomain))
		}
	}
	if pd := s.PanelDomain(); pd != "" && dns.InZone(pd, zone) {
		uses = append(uses, "the panel itself ("+pd+")")
	}
	return uses, nil
}

// DeleteDNSZone deletes a zone with all its records at the provider.
// confirm must repeat the zone's name.
func (s *Service) DeleteDNSZone(ctx context.Context, a Actor, connID int64, zone, confirm string) error {
	if err := needZoneManage(a); err != nil {
		return err
	}
	c, err := s.usableConnection(ctx, a, connID)
	if err != nil {
		return err
	}
	name := normalizeZoneName(zone)
	if normalizeZoneName(confirm) != name || name == "" {
		return errorf(Invalid, "not deleted: type the domain name %s exactly to confirm", name)
	}
	z, err := s.zoneIn(ctx, c, name)
	if err != nil {
		return err
	}
	uses, err := s.zoneUsers(ctx, a, z.Name)
	if err != nil {
		return err
	}
	if len(uses) > 0 {
		return errorf(Conflict, "the domain %s is still in use by %s; remove those first", z.Name, strings.Join(uses, ", "))
	}
	p, token, err := s.provider(c)
	if err != nil {
		return err
	}
	err = p.DeleteZone(ctx, z.Name)
	s.dnsCache.dropZones(c.ID)
	if err != nil {
		derr := dnsError(c.Provider, c.Name, token, err)
		if errors.Is(err, dns.ErrLocked) {
			derr = &Error{Kind: Conflict, Err: err, Msg: fmt.Sprintf("the domain %s is protected against deletion at %s; remove the protection there first", z.Name, providerLabel(c.Provider))}
		}
		s.noteWrite(ctx, c, err, derr)
		return derr
	}
	s.noteWrite(ctx, c, nil, nil)
	s.dnsCache.dropAllChecks()
	s.record(ctx, a, "dns.zone.delete", z.Name, fmt.Sprintf("connection %q", c.Name))
	return nil
}
