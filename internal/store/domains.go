package store

import (
	"context"
	"errors"
	"time"

	"github.com/danielgtmn/ablage"
)

// Domain is a domain a person entered by hand; its DNS is managed elsewhere.
type Domain struct {
	Domain    string
	CreatedAt time.Time
}

// AddDomain records a domain for a user. It reports whether the domain was
// new; adding it again changes nothing.
func (s *Store) AddDomain(ctx context.Context, userID, domain string) (bool, error) {
	err := s.r.domains.Create(ctx, &domainRow{UserID: userID, Domain: domain, CreatedAt: s.now()})
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ablage.ErrDuplicate):
		return false, nil
	case errors.Is(err, ablage.ErrReference):
		return false, errUserNotFound
	}
	return false, err
}

// RemoveDomain forgets a domain of a user.
func (s *Store) RemoveDomain(ctx context.Context, userID, domain string) error {
	n, err := s.r.domains.DeleteWhere(ctx, ablage.And(ablage.Eq("user_id", userID), ablage.Eq("domain", domain)))
	if err != nil {
		return err
	}
	if n == 0 {
		return notFoundf("the domain %s is not in your panel", domain)
	}
	return nil
}

// Domains lists the domains of one user, ordered by name.
func (s *Store) Domains(ctx context.Context, userID string) ([]Domain, error) {
	rows, err := s.r.domains.Find(ctx, ablage.Query{Where: ablage.Eq("user_id", userID), OrderBy: []ablage.Order{ablage.Asc("domain")}})
	if err != nil {
		return nil, err
	}
	out := make([]Domain, len(rows))
	for i, d := range rows {
		out[i] = Domain{Domain: d.Domain, CreatedAt: d.CreatedAt}
	}
	return out, nil
}
