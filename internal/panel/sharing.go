package panel

import (
	"context"
	"errors"
	"strings"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// Shared mailboxes, delegation and distribution groups (G3 in
// wiki/Groupware-Architecture.md).
//
// Who may do what (the checks are all here):
//   - Making a mailbox shared or personal, keeping copies of sent mail, and
//     giving or taking access to a shared mailbox: whoever may change the
//     mailbox (its domain's owner, admins; needMailWrite on a visible
//     mailbox).
//   - Access is only ever given to personal mailboxes of the same
//     organisation (config.Domain.Organisation; a domain without one is its
//     own). Admins cannot cross that line either. A mailbox outside the
//     organisation answers like one that does not exist.
//   - A personal mailbox is shared by the person themselves, from the
//     self-service area (recorded from the mail service's events). The
//     domain owner and admins see those grants and may take them away
//     (people who left), but not give them.
//   - Distribution groups are aliases with a sender policy: whoever may
//     change the alias.

// MailboxGrant is a mailbox that has access to the mailbox shown.
type MailboxGrant struct {
	MailboxID   string   `json:"mailbox_id"`
	Address     string   `json:"address"`
	DisplayName string   `json:"display_name"`
	Rights      []string `json:"rights"`
	// Active is false when the grant does not count (the two mailboxes are
	// no longer in the same organisation).
	Active bool `json:"active"`
}

// MailboxAccess is a mailbox the mailbox shown has access to.
type MailboxAccess struct {
	MailboxID string   `json:"mailbox_id"`
	Address   string   `json:"address"`
	Kind      string   `json:"kind"`
	Rights    []string `json:"rights"`
	Active    bool     `json:"active"`
}

// AliasGroup are the distribution group settings of an alias.
type AliasGroup struct {
	// SenderPolicy is config.SenderAnyone ("" too), SenderOrganisation or
	// SenderMembers.
	SenderPolicy      string
	HideFromDirectory bool
}

// mailboxKind normalizes a kind; "" is personal.
func mailboxKind(k string) (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(k)); v {
	case "", config.KindPersonal:
		return config.KindPersonal, nil
	case config.KindShared:
		return config.KindShared, nil
	}
	return "", errorf(Invalid, "a mailbox is personal or shared")
}

// senderPolicy normalizes a sender policy; "" is anyone.
func senderPolicy(p string) (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(p)); v {
	case "", config.SenderAnyone:
		return config.SenderAnyone, nil
	case config.SenderOrganisation, config.SenderMembers:
		return v, nil
	}
	return "", errorf(Invalid, "who may write to the address is anyone, the organisation or the members")
}

// grantRights checks and normalizes the rights of a grant.
func grantRights(names []string) ([]string, error) {
	clean := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n != "" && !contains(clean, n) {
			clean = append(clean, n)
		}
	}
	r, err := config.ParseRights(clean)
	if err != nil {
		return nil, errorf(Invalid, "choose what the mailbox may do: read, write, manage, send-as, send-on-behalf")
	}
	return r.Names(), nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// organisations maps every mail domain to its organisation as the mail
// service sees it.
func (s *Service) organisations(ctx context.Context) (map[string]string, error) {
	domains, err := s.store.MailDomains(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	out := make(map[string]string, len(domains))
	for _, d := range domains {
		out[d.Domain] = d.OrganisationID
		if d.OrganisationID == "" {
			out[d.Domain] = config.StandaloneOrganisation(d.Domain)
		}
	}
	return out, nil
}

// addSharing fills the grants of the views: who has access to each
// mailbox, and what each has access to.
func (s *Service) addSharing(ctx context.Context, views []Mailbox) ([]Mailbox, error) {
	grants, err := s.store.MailboxGrants(ctx, "", "")
	if err != nil {
		return nil, storeError(err)
	}
	orgs, err := s.organisations(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]int, len(views))
	for i := range views {
		byID[views[i].ID] = i
	}
	for _, g := range grants {
		active := orgs[g.MailboxDomain] != "" && orgs[g.MailboxDomain] == orgs[g.GranteeDomain]
		if i, ok := byID[g.MailboxID]; ok {
			views[i].SharedWith = append(views[i].SharedWith, MailboxGrant{
				MailboxID: g.GranteeID, Address: g.GranteeAddress, DisplayName: g.GranteeName, Rights: g.Rights, Active: active,
			})
		}
		if i, ok := byID[g.GranteeID]; ok {
			views[i].AccessTo = append(views[i].AccessTo, MailboxAccess{
				MailboxID: g.MailboxID, Address: g.MailboxAddress, Kind: g.MailboxKind, Rights: g.Rights, Active: active,
			})
		}
	}
	return views, nil
}

// mailboxWithSharing is one mailbox's view with its grants.
func (s *Service) mailboxWithSharing(ctx context.Context, m store.Mailbox, usage map[string]config.MailboxStatus) (Mailbox, error) {
	v, err := s.addSharing(ctx, []Mailbox{mailboxView(m, usage)})
	if err != nil {
		return Mailbox{}, err
	}
	return v[0], nil
}

// SetMailboxKind makes a mailbox shared or personal. A mailbox made shared
// loses its password and app passwords (mail programs and the self-service
// area are signed out) and the access it had to other mailboxes; one made
// personal needs a password.
func (s *Service) SetMailboxKind(ctx context.Context, a Actor, id string, kind, password string) (Mailbox, error) {
	m, err := s.visibleMailbox(ctx, a, id)
	if err != nil {
		return Mailbox{}, err
	}
	if err := needMailWrite(a); err != nil {
		return Mailbox{}, err
	}
	k, err := mailboxKind(kind)
	if err != nil {
		return Mailbox{}, err
	}
	if k == m.Kind {
		return s.mailboxWithSharing(ctx, m, nil)
	}
	addr := m.LocalPart + "@" + m.Domain
	switch k {
	case config.KindShared:
		if password != "" {
			return Mailbox{}, errorf(Invalid, "a shared mailbox has no password")
		}
		// What the mailbox changed itself is recorded first.
		s.catchUpMailEvents(ctx)
		if err := s.store.MakeMailboxShared(ctx, m.ID); err != nil {
			return Mailbox{}, storeError(err)
		}
		s.record(ctx, a, "mail.mailbox.kind", addr, "now shared: its password and app passwords were removed and it was signed out everywhere")
	default:
		if err := validatePassword(password); err != nil {
			return Mailbox{}, err
		}
		hash, err := hashPassword(password)
		if err != nil {
			return Mailbox{}, newError(Internal, err)
		}
		if err := s.store.MakeMailboxPersonal(ctx, m.ID, hash); err != nil {
			return Mailbox{}, storeError(err)
		}
		s.record(ctx, a, "mail.mailbox.kind", addr, "now personal, with a new password")
	}
	next, err := s.store.Mailbox(ctx, m.ID)
	if err != nil {
		return Mailbox{}, storeError(err)
	}
	v, err := s.mailboxWithSharing(ctx, next, nil)
	if err != nil {
		return Mailbox{}, err
	}
	return v, s.mailChanged(ctx, m.Domain)
}

// SetMailboxKeepSentCopy switches whether a shared mailbox keeps a copy of
// the mail sent as or on behalf of it in its Sent folder.
func (s *Service) SetMailboxKeepSentCopy(ctx context.Context, a Actor, id string, on bool) (Mailbox, error) {
	m, err := s.visibleMailbox(ctx, a, id)
	if err != nil {
		return Mailbox{}, err
	}
	if err := needMailWrite(a); err != nil {
		return Mailbox{}, err
	}
	if m.Kind != config.KindShared {
		return Mailbox{}, errorf(Invalid, "only shared mailboxes keep copies of the mail sent for them")
	}
	if m.KeepSentCopy != on {
		if err := s.store.SetMailboxKeepSentCopy(ctx, m.ID, on); err != nil {
			return Mailbox{}, storeError(err)
		}
		detail := "off"
		if on {
			detail = "on"
		}
		s.record(ctx, a, "mail.mailbox.keep-sent-copy", m.LocalPart+"@"+m.Domain, detail)
		m.KeepSentCopy = on
		v, err := s.mailboxWithSharing(ctx, m, nil)
		if err != nil {
			return Mailbox{}, err
		}
		return v, s.mailChanged(ctx, m.Domain)
	}
	return s.mailboxWithSharing(ctx, m, nil)
}

// SetMailboxGrant gives the personal mailbox with address grantee access to
// the shared mailbox id with rights, or changes its rights.
func (s *Service) SetMailboxGrant(ctx context.Context, a Actor, id string, grantee string, rights []string) (Mailbox, error) {
	m, err := s.visibleMailbox(ctx, a, id)
	if err != nil {
		return Mailbox{}, err
	}
	if err := needMailWrite(a); err != nil {
		return Mailbox{}, err
	}
	if m.Kind != config.KindShared {
		return Mailbox{}, errorf(Invalid, "people share a personal mailbox themselves, in their own settings; make the mailbox shared to give access to it here")
	}
	r, err := grantRights(rights)
	if err != nil {
		return Mailbox{}, err
	}
	g, err := s.granteeFor(ctx, m, grantee)
	if err != nil {
		return Mailbox{}, err
	}
	existed, err := s.store.SetMailboxGrant(ctx, m.ID, g.ID, r, config.MaxGrants)
	if err != nil {
		return Mailbox{}, storeError(err)
	}
	action := "mail.mailbox.grant"
	if existed {
		action = "mail.mailbox.grant-change"
	}
	s.record(ctx, a, action, m.LocalPart+"@"+m.Domain, g.LocalPart+"@"+g.Domain+": "+strings.Join(r, ", "))
	v, err := s.mailboxWithSharing(ctx, m, nil)
	if err != nil {
		return Mailbox{}, err
	}
	return v, s.mailChanged(ctx, m.Domain)
}

// granteeFor finds the personal mailbox address in the organisation of m.
// Anything else answers the same, so nothing outside the organisation
// shows.
func (s *Service) granteeFor(ctx context.Context, m store.Mailbox, address string) (store.Mailbox, error) {
	addr := strings.ToLower(strings.TrimSpace(address))
	notHere := errorf(Invalid, "there is no personal mailbox %s in this organisation", strings.TrimSpace(address))
	local, domain, ok := config.SplitAddress(addr)
	if !ok || !config.ValidAddress(addr) {
		return store.Mailbox{}, errorf(Invalid, "%q is not a valid e-mail address", strings.TrimSpace(address))
	}
	g, err := s.store.MailboxByAddress(ctx, domain, local)
	if errors.Is(err, store.ErrNotFound) {
		return store.Mailbox{}, notHere
	}
	if err != nil {
		return store.Mailbox{}, storeError(err)
	}
	orgs, err := s.organisations(ctx)
	if err != nil {
		return store.Mailbox{}, err
	}
	if orgs[g.Domain] == "" || orgs[g.Domain] != orgs[m.Domain] {
		return store.Mailbox{}, notHere
	}
	if g.ID == m.ID {
		return store.Mailbox{}, errorf(Invalid, "a mailbox cannot be given access to itself")
	}
	if g.Kind == config.KindShared {
		return store.Mailbox{}, errorf(Invalid, "%s is a shared mailbox; give access to the people who use it instead", addr)
	}
	return g, nil
}

// RemoveMailboxGrant takes the access of mailbox granteeID to mailbox id
// away (a shared mailbox, or a personal one its owner shared).
func (s *Service) RemoveMailboxGrant(ctx context.Context, a Actor, id, granteeID string) (Mailbox, error) {
	m, err := s.visibleMailbox(ctx, a, id)
	if err != nil {
		return Mailbox{}, err
	}
	if err := needMailWrite(a); err != nil {
		return Mailbox{}, err
	}
	grants, err := s.store.MailboxGrants(ctx, m.ID, "")
	if err != nil {
		return Mailbox{}, storeError(err)
	}
	var found *store.MailboxGrant
	for i := range grants {
		if grants[i].GranteeID == granteeID {
			found = &grants[i]
		}
	}
	if found == nil {
		return Mailbox{}, errorf(NotFound, "this mailbox has no such access")
	}
	if m.Kind != config.KindShared {
		// Recorded first, so a change the person made meanwhile is not
		// pushed back over this one.
		s.catchUpMailEvents(ctx)
	}
	if err := s.store.DeleteMailboxGrant(ctx, m.ID, found.GranteeID); err != nil {
		return Mailbox{}, storeError(err)
	}
	s.record(ctx, a, "mail.mailbox.grant-remove", m.LocalPart+"@"+m.Domain, found.GranteeAddress)
	v, err := s.mailboxWithSharing(ctx, m, nil)
	if err != nil {
		return Mailbox{}, err
	}
	return v, s.mailChanged(ctx, m.Domain)
}

// configGrants are the grants of the configuration, by mailbox id.
func configGrants(grants []store.MailboxGrant) map[string][]config.Grant {
	out := map[string][]config.Grant{}
	for _, g := range grants {
		out[g.MailboxID] = append(out[g.MailboxID], config.Grant{StorageID: g.GranteeStorage, Rights: g.Rights})
	}
	return out
}

// groupDetail describes the group settings of an alias for the activity log.
func groupDetail(policy string, hide bool) string {
	d := ""
	switch policy {
	case config.SenderOrganisation:
		d = "only the organisation may write to it"
	case config.SenderMembers:
		d = "only its members may write to it"
	}
	if hide {
		if d != "" {
			d += "; "
		}
		d += "hidden from the address book"
	}
	return d
}
