package panel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
func (s *Service) CreateDNSZone(ctx context.Context, a Actor, connID string, name string) (DNSZone, error) {
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
	// A created zone is managed in the panel. It exists at the provider
	// either way, so a failure here is only logged.
	v := zoneView(c, z)
	if _, err := s.store.AdoptDNSZone(context.WithoutCancel(ctx), c.ID, z.Name); err != nil {
		slog.Error("could not mark a created DNS zone as managed", "connection", c.ID, "zone", z.Name, "err", err)
	} else {
		v.Adopted = true
	}
	return v, nil
}

// domainUse is one thing that uses a domain: a site, a redirect, a mail
// domain or the panel itself. what describes it for the actor; things the
// actor may not see are described without their names. own is set for the
// actor's own things (and the panel itself for administrators): only those
// keep a zone in the actor's panel, while everyone's block deleting it.
type domainUse struct {
	domains []string
	own     bool
	what    func(in []string) string
}

// domainUses lists everything of every person that uses a domain.
func (s *Service) domainUses(ctx context.Context, a Actor) ([]domainUse, error) {
	var uses []domainUse
	sites, err := s.store.Sites(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	byName := map[string]store.Site{}
	for _, site := range sites {
		byName[site.Name] = site
		if len(site.Domains) == 0 {
			continue
		}
		name, visible := site.Name, a.canSee(site)
		own := visible && a.UserID != 0 && site.OwnerID == a.UserID
		uses = append(uses, domainUse{domains: site.Domains, own: own, what: func(in []string) string {
			if visible {
				return fmt.Sprintf("the site %s (%s)", name, strings.Join(in, ", "))
			}
			return fmt.Sprintf("a site of another person (%s)", strings.Join(in, ", "))
		}})
	}
	redirects, err := s.store.Redirects(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	for _, r := range redirects {
		if r.SourcePath != "" {
			continue // path redirects are on site domains, listed above
		}
		d, visible := r.SourceDomain, a.canSeeRedirect(r, byName)
		own := visible && a.UserID != 0 && r.OwnerID == a.UserID
		uses = append(uses, domainUse{domains: []string{d}, own: own, what: func([]string) string {
			if visible {
				return "the redirect of " + d
			}
			return fmt.Sprintf("a redirect of another person (%s)", d)
		}})
	}
	mailDomains, err := s.store.MailDomains(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	for _, md := range mailDomains {
		d, visible := md.Domain, a.canSeeMail(md.OwnerID)
		own := visible && a.UserID != 0 && md.OwnerID == a.UserID
		uses = append(uses, domainUse{domains: []string{d}, own: own, what: func([]string) string {
			if visible {
				return "e-mail for " + d
			}
			return fmt.Sprintf("e-mail of another person (%s)", d)
		}})
	}
	if pd := s.PanelDomain(); pd != "" {
		own := a.Admin && a.Sites == nil
		uses = append(uses, domainUse{domains: []string{pd}, own: own, what: func([]string) string {
			return "the panel itself (" + pd + ")"
		}})
	}
	return uses, nil
}

// usersOf describes what of uses has a domain inside zone; with ownOnly,
// only the actor's own things.
func usersOf(uses []domainUse, zone string, ownOnly bool) []string {
	out := []string{}
	for _, u := range uses {
		if ownOnly && !u.own {
			continue
		}
		var in []string
		for _, d := range u.domains {
			if dns.InZone(d, zone) {
				in = append(in, d)
			}
		}
		if len(in) > 0 {
			out = append(out, u.what(in))
		}
	}
	return out
}

// zoneUsers lists what uses a domain inside zone: sites, redirects, e-mail
// and the panel itself, across every person, or only the actor's own
// (ownOnly), which is what keeps a zone in the actor's panel.
func (s *Service) zoneUsers(ctx context.Context, a Actor, zone string, ownOnly bool) ([]string, error) {
	uses, err := s.domainUses(ctx, a)
	if err != nil {
		return nil, err
	}
	return usersOf(uses, zone, ownOnly), nil
}

// DeleteDNSZone deletes a zone with all its records at the provider.
// confirm must repeat the zone's name.
func (s *Service) DeleteDNSZone(ctx context.Context, a Actor, connID string, zone, confirm string) error {
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
	uses, err := s.zoneUsers(ctx, a, z.Name, false)
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
	if err := s.store.ReleaseDNSZone(context.WithoutCancel(ctx), c.ID, z.Name); err != nil && !errors.Is(err, store.ErrNotFound) {
		slog.Error("could not forget a deleted DNS zone", "connection", c.ID, "zone", z.Name, "err", err)
	}
	s.record(ctx, a, "dns.zone.delete", z.Name, fmt.Sprintf("connection %q", c.Name))
	return nil
}

// ---- choosing which zones are managed in the panel ----

type zoneKey struct {
	conn int64
	zone string
}

// adoptedZones are the zones the actor chose to manage in the panel.
func (s *Service) adoptedZones(ctx context.Context, a Actor) (map[zoneKey]bool, error) {
	list, err := s.store.AdoptedDNSZones(ctx, a.UserID)
	if err != nil {
		return nil, storeError(err)
	}
	out := make(map[zoneKey]bool, len(list))
	for _, z := range list {
		out[zoneKey{z.ConnectionID, z.Zone}] = true
	}
	return out, nil
}

// AdoptDNSZone starts managing a zone of one of the actor's own connections
// in the panel. Nothing changes at the provider. Adopting a zone again is
// fine.
func (s *Service) AdoptDNSZone(ctx context.Context, a Actor, connID string, zone string) (DNSZone, error) {
	if err := needDNSManage(a); err != nil {
		return DNSZone{}, err
	}
	c, err := s.usableConnection(ctx, a, connID)
	if err != nil {
		return DNSZone{}, err
	}
	z, err := s.zoneIn(ctx, c, zone)
	if err != nil {
		return DNSZone{}, err
	}
	created, err := s.store.AdoptDNSZone(ctx, c.ID, z.Name)
	if err != nil {
		return DNSZone{}, storeError(err)
	}
	if created {
		s.record(ctx, a, "dns.zone.adopt", z.Name, fmt.Sprintf("connection %q", c.Name))
	}
	v := zoneView(c, z)
	v.Adopted = true
	if v.UsedBy, err = s.zoneUsers(ctx, a, z.Name, true); err != nil {
		return DNSZone{}, err
	}
	return v, nil
}

// ReleaseDNSZone stops managing a zone in the panel; nothing is deleted at
// the provider. A zone that the actor's own things use stays in the panel
// anyway, so it is refused with what uses it.
func (s *Service) ReleaseDNSZone(ctx context.Context, a Actor, connID string, zone string) error {
	if err := needDNSManage(a); err != nil {
		return err
	}
	c, err := s.usableConnection(ctx, a, connID)
	if err != nil {
		return err
	}
	name := normalizeZoneName(zone)
	uses, err := s.zoneUsers(ctx, a, name, true)
	if err != nil {
		return err
	}
	if len(uses) > 0 {
		return errorf(Conflict, "the domain %s stays in the panel while it is used by %s", name, strings.Join(uses, ", "))
	}
	if err := s.store.ReleaseDNSZone(ctx, c.ID, name); err != nil {
		return storeError(err)
	}
	s.record(ctx, a, "dns.zone.release", name, fmt.Sprintf("connection %q", c.Name))
	return nil
}
