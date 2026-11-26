package panel

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/danielgtmn/ownpanel/internal/store"
)

// othersMailDomainName refuses, for members, a domain that is somebody
// else's mail domain or one of its names for setting up mail programs
// (autoconfig./autodiscover.). Claiming them as a site or domain redirect
// would take over that person's mail setup. Admins may use any domain, as
// for mail (mayUseMailDomain). Domains in keep (what the site or redirect
// had before) stay allowed, so an earlier claim can still be edited.
func (s *Service) othersMailDomainName(ctx context.Context, a Actor, domains, keep []string) error {
	if a.Admin {
		return nil
	}
	all, err := s.store.MailDomains(ctx)
	if err != nil {
		return storeError(err)
	}
	for _, d := range domains {
		if slices.Contains(keep, d) {
			continue
		}
		for _, m := range all {
			if m.OwnerID == a.UserID {
				continue
			}
			if d == m.Domain || slices.Contains(setupNames(m.Domain), d) {
				return errorf(Invalid, "the domain %s is already used by another person", d)
			}
		}
	}
	return nil
}

// othersAccountAddressIn reports whether a user other than userID signs in
// with an e-mail address in domain or one below it. Mail for that address
// must not reach somebody else's mailbox.
func (s *Service) othersAccountAddressIn(ctx context.Context, userID int64, domain string) (bool, error) {
	users, err := s.store.Users(ctx)
	if err != nil {
		return false, storeError(err)
	}
	auth, err := s.authUsers(ctx)
	if err != nil {
		return false, err
	}
	for _, u := range users {
		if u.ID == userID {
			continue
		}
		if d := emailDomain(auth[u.AuthUserID].Email); d != "" && (d == domain || strings.HasSuffix(d, "."+domain)) {
			return true, nil
		}
	}
	return false, nil
}

// emailDomain is the domain of an address, lower case; "" if there is none.
func emailDomain(email string) string {
	i := strings.LastIndexByte(email, '@')
	if i < 0 {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(email[i+1:]), ".")
}

// errResetMailboxNotTheirs: the reset link would go to a mailbox here that
// belongs to somebody else.
var errResetMailboxNotTheirs = errors.New("the address of the account is in a mail domain of another member here; the reset link was not sent")

// mayMailResetTo checks that a reset link for the account with address to
// is not delivered into a mailbox here that somebody else controls: when
// the address's domain is a mail domain here, its owner must be the
// account holder or an admin.
func (s *Service) mayMailResetTo(ctx context.Context, to string) error {
	d := emailDomain(to)
	if d == "" {
		return nil
	}
	md, err := s.store.MailDomain(ctx, d)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	owner, err := s.store.User(ctx, md.OwnerID)
	if err != nil {
		return err
	}
	if owner.Role == store.RoleAdmin {
		return nil
	}
	au, err := s.auth.UserByEmail(ctx, to)
	if err != nil {
		return err
	}
	holder, err := s.store.UserByAuthID(ctx, au.ID)
	if err != nil {
		return err
	}
	if holder.ID != owner.ID {
		return errResetMailboxNotTheirs
	}
	return nil
}
