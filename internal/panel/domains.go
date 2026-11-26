package panel

import (
	"context"
	"time"
)

// Domains a person entered by hand because their DNS is managed elsewhere
// (another provider, the registrar). An entry keeps the domain in the
// person's panel as a domain of its own; nothing at any DNS provider
// belongs to it. Entries are per person: someone else's is not found, and
// administrators see only their own.

// Domain is a domain the actor entered by hand.
type Domain struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	// UsedBy describes what of the actor's own uses a name in the domain
	// (sites, redirects, e-mail; for administrators the panel itself). Such
	// a domain stays in the panel through that use even without the entry.
	UsedBy []string `json:"used_by"`
}

// RemovedDomain is the outcome of removing an entry.
type RemovedDomain struct {
	Name string `json:"name"`
	// UsedBy: what of the actor's own still uses a name in the domain, so it
	// stays in the panel anyway.
	UsedBy []string `json:"used_by"`
}

// needDomains: a person, not through a token limited to sites.
func needDomains(a Actor) error {
	if err := needAuth(a); err != nil {
		return err
	}
	if a.Via == ViaSystem || a.UserID == 0 {
		return errorf(Forbidden, "domains can only be added by a person")
	}
	if a.Sites != nil {
		return errorf(Forbidden, "this access token is limited to certain sites and cannot see or change domains")
	}
	return nil
}

// needDomainsWrite: as needDomains, signed in or with a token that may
// write to all sites.
func needDomainsWrite(a Actor) error {
	if err := needDomains(a); err != nil {
		return err
	}
	if a.ReadOnly {
		return errorf(Forbidden, "adding and removing domains needs a password sign-in or an access token with write access to all sites")
	}
	return nil
}

// Domains lists the domains the actor entered by hand.
func (s *Service) Domains(ctx context.Context, a Actor) ([]Domain, error) {
	if err := needDomains(a); err != nil {
		return nil, err
	}
	list, err := s.store.Domains(ctx, a.UserID)
	if err != nil {
		return nil, storeError(err)
	}
	uses, err := s.domainUses(ctx, a)
	if err != nil {
		return nil, err
	}
	out := make([]Domain, 0, len(list))
	for _, d := range list {
		out = append(out, Domain{Name: d.Domain, CreatedAt: d.CreatedAt, UsedBy: usersOf(uses, d.Domain, true)})
	}
	return out, nil
}

// AddDomain keeps a domain whose DNS is managed elsewhere in the actor's
// panel. name must be a registrable domain, like example.com. Adding it
// again is fine.
func (s *Service) AddDomain(ctx context.Context, a Actor, name string) (Domain, error) {
	if err := needDomainsWrite(a); err != nil {
		return Domain{}, err
	}
	d, err := newZoneName(name)
	if err != nil {
		return Domain{}, err
	}
	created, err := s.store.AddDomain(ctx, a.UserID, d)
	if err != nil {
		return Domain{}, storeError(err)
	}
	if created {
		s.record(ctx, a, "domain.add", d, "DNS managed elsewhere")
	}
	list, err := s.store.Domains(ctx, a.UserID)
	if err != nil {
		return Domain{}, storeError(err)
	}
	out := Domain{Name: d}
	for _, x := range list {
		if x.Domain == d {
			out.CreatedAt = x.CreatedAt
		}
	}
	if out.UsedBy, err = s.zoneUsers(ctx, a, d, true); err != nil {
		return Domain{}, err
	}
	return out, nil
}

// RemoveDomain removes the actor's entry for a domain; nothing changes at
// any DNS provider. When the actor's own things still use a name in the
// domain, it stays in the panel through them; the result says what.
func (s *Service) RemoveDomain(ctx context.Context, a Actor, name string) (RemovedDomain, error) {
	if err := needDomainsWrite(a); err != nil {
		return RemovedDomain{}, err
	}
	d := normalizeZoneName(name)
	if err := s.store.RemoveDomain(ctx, a.UserID, d); err != nil {
		return RemovedDomain{}, storeError(err)
	}
	s.record(ctx, a, "domain.remove", d, "")
	uses, err := s.zoneUsers(ctx, a, d, true)
	if err != nil {
		return RemovedDomain{}, err
	}
	return RemovedDomain{Name: d, UsedBy: uses}, nil
}
