package panel

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/passwd"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// Mailbox accounts (G1 in wiki/Groupware-Architecture.md): display names,
// organisations and app passwords.
//
// Who may do what:
//   - display name, "mail programs only with app passwords", app passwords:
//     whoever may change the mailbox (needMailWrite on a visible mailbox).
//     App passwords are created only with a password sign-in or an
//     unrestricted write token; the MCP server has no tool for them, so a
//     token never passes through an AI agent. The token is answered once;
//     afterwards only its name, creation and last use are shown.
//   - organisations: a member creates organisations of their own and puts
//     their own domains into them. Grouping domains of different owners
//     (or putting a domain into an organisation of someone else) is for
//     admins only and recorded as such. Anyone who may change a domain may
//     take it out of an organisation again.
//
// The mailbox itself (self-service in the mail service, built later) can
// change its password, display name, "only app passwords" and app
// passwords. The mail service applies such a change at once and reports it
// as an event with a revision; recordMailEvents applies it here (see
// applyAccountEvent).

const (
	// MaxOrganisationsPerUser bounds the organisations of a member.
	MaxOrganisationsPerUser = 20
	// keyAppPasswordKey is the setting with the sealed app password key.
	keyAppPasswordKey = "mail_app_password_key"
	// appPasswordKeyPurpose binds the sealed key to its use.
	appPasswordKeyPurpose = "ownpanel mail app password key"
)

// ---- names ----

// displayName normalizes and checks a display name (empty allowed).
func displayName(v string) (string, error) {
	// Runs of spaces fold into one; line breaks and other control
	// characters are refused, not folded, so nobody is surprised.
	d := strings.Join(strings.FieldsFunc(v, func(r rune) bool { return r == ' ' }), " ")
	if !config.ValidDisplayName(d) {
		return "", errorf(Invalid, "the name can have at most %d characters, without line breaks and without < > or \"", config.MaxDisplayName)
	}
	return d, nil
}

// shortName is displayName that must not be empty (organisations, app
// passwords).
func shortName(v, what string) (string, error) {
	d, err := displayName(v)
	if err != nil {
		return "", err
	}
	if d == "" {
		return "", errorf(Invalid, "enter a name for the %s", what)
	}
	return d, nil
}

// ---- organisations ----

// Organisation is what callers see of an organisation.
type Organisation struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Owner     string    `json:"owner"`
	Domains   []string  `json:"domains"`
	CreatedAt time.Time `json:"created_at"`
}

// MailDomainOrganisation is the organisation of a mail domain as the
// domain's view shows it; nil when the domain stands alone.
type MailDomainOrganisation struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Own: the person looking may manage the organisation (they own it or
	// are an admin). Otherwise an admin put the domain into it.
	Own bool `json:"own"`
	// Domains are the organisation's other domains the person may see.
	Domains []string `json:"domains"`
}

func organisationView(o store.Organisation) Organisation {
	return Organisation{ID: o.ID, Name: o.Name, Owner: o.Owner, Domains: o.Domains, CreatedAt: o.CreatedAt}
}

func (a Actor) canSeeOrganisation(o store.Organisation) bool { return a.canSeeMail(o.OwnerID) }

// visibleOrganisation returns an organisation the actor may see; others
// answer NotFound like missing ones.
func (s *Service) visibleOrganisation(ctx context.Context, a Actor, id string) (store.Organisation, error) {
	if err := needMailRead(a); err != nil {
		return store.Organisation{}, err
	}
	o, err := s.store.Organisation(ctx, strings.TrimSpace(id))
	if errors.Is(err, store.ErrNotFound) || (err == nil && !a.canSeeOrganisation(o)) {
		return store.Organisation{}, errorf(NotFound, "organisation not found")
	}
	if err != nil {
		return store.Organisation{}, storeError(err)
	}
	return o, nil
}

// Organisations lists the organisations the actor may manage (admins: all).
func (s *Service) Organisations(ctx context.Context, a Actor) ([]Organisation, error) {
	if err := needMailRead(a); err != nil {
		return nil, err
	}
	all, err := s.store.Organisations(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	out := []Organisation{}
	for _, o := range all {
		if a.canSeeOrganisation(o) {
			out = append(out, organisationView(o))
		}
	}
	return out, nil
}

// CreateOrganisation makes an organisation owned by the actor; domains are
// put into it with SetMailDomainOrganisation.
func (s *Service) CreateOrganisation(ctx context.Context, a Actor, name string) (Organisation, error) {
	if err := needMailWrite(a); err != nil {
		return Organisation{}, err
	}
	n, err := shortName(name, "organisation")
	if err != nil {
		return Organisation{}, err
	}
	limit := MaxOrganisationsPerUser
	if a.Admin {
		limit = 0
	}
	o, err := s.store.CreateOrganisation(ctx, "org-"+newStorageID()[:16], a.UserID, n, limit)
	if err != nil {
		return Organisation{}, storeError(err)
	}
	s.record(ctx, a, "mail.organisation.create", n, "")
	return organisationView(o), nil
}

// RenameOrganisation changes an organisation's name.
func (s *Service) RenameOrganisation(ctx context.Context, a Actor, id, name string) (Organisation, error) {
	o, err := s.visibleOrganisation(ctx, a, id)
	if err != nil {
		return Organisation{}, err
	}
	if err := needMailWrite(a); err != nil {
		return Organisation{}, err
	}
	n, err := shortName(name, "organisation")
	if err != nil {
		return Organisation{}, err
	}
	if n == o.Name {
		return organisationView(o), nil
	}
	if err := s.store.RenameOrganisation(ctx, o.ID, n); err != nil {
		return Organisation{}, storeError(err)
	}
	s.record(ctx, a, "mail.organisation.rename", n, "was "+o.Name)
	o.Name = n
	return organisationView(o), nil
}

// DeleteOrganisation removes an organisation; its domains then stand alone.
func (s *Service) DeleteOrganisation(ctx context.Context, a Actor, id string) error {
	o, err := s.visibleOrganisation(ctx, a, id)
	if err != nil {
		return err
	}
	if err := needMailWrite(a); err != nil {
		return err
	}
	if err := s.store.DeleteOrganisation(ctx, o.ID); err != nil {
		return storeError(err)
	}
	detail := ""
	if len(o.Domains) > 0 {
		detail = "its domains now stand alone: " + strings.Join(o.Domains, ", ")
	}
	s.record(ctx, a, "mail.organisation.delete", o.Name, detail)
	if len(o.Domains) == 0 {
		return nil
	}
	return s.mailSaved(ctx)
}

// SetMailDomainOrganisation puts a mail domain into an organisation, or
// with orgID "" lets it stand alone again.
func (s *Service) SetMailDomainOrganisation(ctx context.Context, a Actor, domain, orgID string) (MailDomain, error) {
	md, err := s.visibleMailDomain(ctx, a, domain)
	if err != nil {
		return MailDomain{}, err
	}
	if err := needMailWrite(a); err != nil {
		return MailDomain{}, err
	}
	orgID = strings.TrimSpace(orgID)
	if orgID == md.OrganisationID {
		return s.mailDomainViewFor(ctx, a, md)
	}
	detail := "stands alone"
	if orgID != "" {
		o, err := s.visibleOrganisation(ctx, a, orgID)
		if err != nil {
			return MailDomain{}, err
		}
		cross := o.OwnerID != md.OwnerID
		for _, d := range o.Domains {
			other, err := s.store.MailDomain(ctx, d)
			if err != nil {
				return MailDomain{}, storeError(err)
			}
			cross = cross || other.OwnerID != md.OwnerID
		}
		if cross && !a.administers() {
			return MailDomain{}, errorf(Forbidden, "only administrators can group domains of different owners")
		}
		detail = "into " + o.Name
		if len(o.Domains) > 0 {
			detail += " with " + strings.Join(o.Domains, ", ")
		}
		if cross {
			detail += " (domains of different owners)"
		}
	}
	if err := s.store.SetMailDomainOrganisation(ctx, md.Domain, orgID); err != nil {
		return MailDomain{}, storeError(err)
	}
	s.record(ctx, a, "mail.domain.organisation", md.Domain, detail)
	md, err = s.store.MailDomain(ctx, md.Domain)
	if err != nil {
		return MailDomain{}, storeError(err)
	}
	v, err := s.mailDomainViewFor(ctx, a, md)
	if err != nil {
		return MailDomain{}, err
	}
	if md.Active {
		return v, s.mailSaved(ctx)
	}
	return v, nil
}

// mailDomainViewFor is mailDomainView with the organisation as a sees it.
func (s *Service) mailDomainViewFor(ctx context.Context, a Actor, d store.MailDomain) (MailDomain, error) {
	v, err := s.mailDomainView(ctx, d)
	if err != nil || d.OrganisationID == "" {
		return v, err
	}
	o, err := s.store.Organisation(ctx, d.OrganisationID)
	if err != nil {
		return MailDomain{}, storeError(err)
	}
	org := &MailDomainOrganisation{ID: o.ID, Name: o.Name, Own: a.canSeeOrganisation(o), Domains: []string{}}
	for _, other := range o.Domains {
		if other == d.Domain {
			continue
		}
		if od, err := s.store.MailDomain(ctx, other); err == nil && a.canSeeMail(od.OwnerID) {
			org.Domains = append(org.Domains, other)
		}
	}
	v.Organisation = org
	return v, nil
}

// ---- display name and "only app passwords" ----

// MailboxAccount changes a mailbox's display name and whether mail
// programs need app passwords; nil fields stay.
type MailboxAccount struct {
	DisplayName      *string
	AppPasswordsOnly *bool
}

// SetMailboxAccount changes the display name and "mail programs only with
// app passwords" of a mailbox.
func (s *Service) SetMailboxAccount(ctx context.Context, a Actor, id int64, in MailboxAccount) (Mailbox, error) {
	m, err := s.visibleMailbox(ctx, a, id)
	if err != nil {
		return Mailbox{}, err
	}
	if err := needMailWrite(a); err != nil {
		return Mailbox{}, err
	}
	next := m
	if in.DisplayName != nil {
		if next.DisplayName, err = displayName(*in.DisplayName); err != nil {
			return Mailbox{}, err
		}
	}
	if in.AppPasswordsOnly != nil {
		next.AppPasswordsOnly = *in.AppPasswordsOnly
	}
	if next.DisplayName == m.DisplayName && next.AppPasswordsOnly == m.AppPasswordsOnly {
		return mailboxView(m, nil), nil
	}
	// What the mailbox changed itself is recorded first, so the push below
	// includes it (see catchUpMailEvents).
	s.catchUpMailEvents(ctx)
	// Only the fields that change are written (and get the admin's time),
	// so the mailbox's own change of the other one is not discarded.
	var dn *string
	var only *bool
	if next.DisplayName != m.DisplayName {
		dn = &next.DisplayName
	}
	if next.AppPasswordsOnly != m.AppPasswordsOnly {
		only = &next.AppPasswordsOnly
	}
	if err := s.store.SetMailboxAccount(ctx, id, dn, only); err != nil {
		return Mailbox{}, storeError(err)
	}
	addr := m.LocalPart + "@" + m.Domain
	if next.DisplayName != m.DisplayName {
		detail := "removed"
		if next.DisplayName != "" {
			detail = "now " + next.DisplayName
		}
		s.record(ctx, a, "mail.mailbox.display-name", addr, detail)
	}
	if next.AppPasswordsOnly != m.AppPasswordsOnly {
		detail := "off"
		if next.AppPasswordsOnly {
			detail = "on"
		}
		s.record(ctx, a, "mail.mailbox.app-passwords-only", addr, detail)
	}
	return mailboxView(next, nil), s.mailChanged(ctx, m.Domain)
}

// ---- app passwords ----

// AppPassword is what callers see of an app password: never the token.
type AppPassword struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	// LastUsedNetwork is the network (/24 or /48) of the last use.
	LastUsedNetwork string `json:"last_used_network"`
}

// NewAppPassword is a created app password with its token, which is shown
// this once and never stored.
type NewAppPassword struct {
	AppPassword
	Password string `json:"password"`
}

func appPasswordView(a store.AppPassword) AppPassword {
	return AppPassword{ID: a.ID, Name: a.Name, CreatedAt: a.CreatedAt, LastUsedAt: a.LastUsedAt, LastUsedNetwork: a.LastUsedNet}
}

// AppPasswords lists a mailbox's app passwords, oldest first.
func (s *Service) AppPasswords(ctx context.Context, a Actor, mailboxID int64) ([]AppPassword, error) {
	if _, err := s.visibleMailbox(ctx, a, mailboxID); err != nil {
		return nil, err
	}
	list, err := s.store.AppPasswords(ctx, mailboxID)
	if err != nil {
		return nil, storeError(err)
	}
	out := []AppPassword{}
	for _, ap := range list {
		out = append(out, appPasswordView(ap))
	}
	return out, nil
}

// CreateAppPassword makes an app password for a mail program or device and
// returns it with the token, which is never shown again.
func (s *Service) CreateAppPassword(ctx context.Context, a Actor, mailboxID int64, name string) (NewAppPassword, error) {
	m, err := s.visibleMailbox(ctx, a, mailboxID)
	if err != nil {
		return NewAppPassword{}, err
	}
	if err := needMailWrite(a); err != nil {
		return NewAppPassword{}, err
	}
	n, err := shortName(name, "app password (the device or program it is for)")
	if err != nil {
		return NewAppPassword{}, err
	}
	key, err := s.appPasswordKey(ctx)
	if err != nil {
		return NewAppPassword{}, err
	}
	id, secret, token := config.NewAppPassword()
	saved, err := s.store.CreateAppPassword(ctx, store.AppPassword{
		ID: id, MailboxID: m.ID, Name: n, MAC: config.AppPasswordMAC(key, m.StorageID, id, secret),
	}, config.MaxAppPasswords)
	if err != nil {
		return NewAppPassword{}, storeError(err)
	}
	s.record(ctx, a, "mail.mailbox.app-password-create", m.LocalPart+"@"+m.Domain, n)
	return NewAppPassword{AppPassword: appPasswordView(saved), Password: token}, s.mailChanged(ctx, m.Domain)
}

// RevokeAppPassword deletes an app password; mail programs using it are
// signed out.
func (s *Service) RevokeAppPassword(ctx context.Context, a Actor, mailboxID int64, id string) error {
	m, err := s.visibleMailbox(ctx, a, mailboxID)
	if err != nil {
		return err
	}
	if err := needMailWrite(a); err != nil {
		return err
	}
	ap, err := s.store.DeleteAppPassword(ctx, m.ID, strings.TrimSpace(id))
	if errors.Is(err, store.ErrNotFound) {
		return errorf(NotFound, "app password not found")
	}
	if err != nil {
		return storeError(err)
	}
	s.record(ctx, a, "mail.mailbox.app-password-revoke", m.LocalPart+"@"+m.Domain, ap.Name)
	return s.mailChanged(ctx, m.Domain)
}

// appPasswordKey returns the server key app passwords are checked with
// (hex), making and storing it (sealed) the first time. It is pushed to the
// mail service in the configuration; the panel needs it to create app
// passwords.
func (s *Service) appPasswordKey(ctx context.Context) (string, error) {
	s.appKeyMu.Lock()
	defer s.appKeyMu.Unlock()
	if s.appKey != "" {
		return s.appKey, nil
	}
	if s.secrets == nil {
		return "", errorf(Internal, "no secret key configured; app passwords cannot be used")
	}
	m, err := s.store.Settings(ctx)
	if err != nil {
		return "", storeError(err)
	}
	if v := m[keyAppPasswordKey]; v != "" {
		sealed, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return "", newError(Internal, errors.New("the stored app password key is damaged"))
		}
		key, err := s.secrets.Open(sealed, appPasswordKeyPurpose)
		if err != nil {
			return "", newError(Internal, errors.New("the app password key cannot be read (has the server's secret key changed?)"))
		}
		if _, err := hex.DecodeString(string(key)); err != nil || len(key) != 2*config.AppPasswordKeyBytes {
			return "", newError(Internal, errors.New("the stored app password key is damaged"))
		}
		s.appKey = string(key)
		return s.appKey, nil
	}
	key := config.NewAppPasswordKey()
	sealed := base64.StdEncoding.EncodeToString(s.secrets.Seal([]byte(key), appPasswordKeyPurpose))
	if err := s.store.PutSettings(ctx, map[string]string{keyAppPasswordKey: sealed}); err != nil {
		return "", storeError(err)
	}
	s.appKey = key
	return key, nil
}

// ---- changes reported by the mail service ----

// accountEventKinds are the events applyAccountEvent handles.
var accountEventKinds = map[string]bool{
	config.EventPasswordChanged:    true,
	config.EventDisplayNameChanged: true,
	config.EventAppPasswordsOnly:   true,
	config.EventAppPasswordCreated: true,
	config.EventAppPasswordRevoked: true,
	config.EventAppPasswordUsed:    true,
}

// applyAccountEvent records a change a mailbox made to its own account in
// the mail service (or the use of an app password). It reports whether the
// configuration changed, so it must be pushed. Events of unknown
// mailboxes, malformed ones and revisions recorded already are ignored;
// the activity log gets an entry for every applied change (never a hash or
// MAC).
func (s *Service) applyAccountEvent(ctx context.Context, e config.Event) (bool, error) {
	if s.failAccountEvent != nil {
		if err := s.failAccountEvent(e); err != nil {
			return false, err
		}
	}
	ch := e.Account
	if ch == nil || !config.ValidStorageID(e.StorageID) {
		return false, nil
	}
	at := e.At
	if at.IsZero() || at.After(s.now()) {
		at = s.now()
	}
	if e.Kind == config.EventAppPasswordUsed {
		if !config.ValidAppPasswordID(ch.AppPasswordID) || len(ch.Network) > 64 {
			return false, nil
		}
		return false, s.store.TouchAppPassword(ctx, e.StorageID, ch.AppPasswordID, at, ch.Network)
	}
	if ch.Revision <= 0 {
		return false, nil
	}
	mc := store.MailboxChange{Revision: ch.Revision, At: at}
	detail := ""
	switch e.Kind {
	case config.EventPasswordChanged:
		if ch.PasswordHash == "" || !passwd.WellFormed(ch.PasswordHash) {
			return false, nil
		}
		mc.PasswordHash = &ch.PasswordHash
	case config.EventDisplayNameChanged:
		if ch.DisplayName == nil || !config.ValidDisplayName(*ch.DisplayName) {
			return false, nil
		}
		mc.DisplayName = ch.DisplayName
		detail = "removed"
		if *ch.DisplayName != "" {
			detail = "now " + *ch.DisplayName
		}
	case config.EventAppPasswordsOnly:
		if ch.AppPasswordsOnly == nil {
			return false, nil
		}
		mc.AppPasswordsOnly = ch.AppPasswordsOnly
		detail = "off"
		if *ch.AppPasswordsOnly {
			detail = "on"
		}
	case config.EventAppPasswordCreated:
		ap := ch.AppPassword
		if ap == nil || !config.ValidAppPasswordID(ap.ID) || ap.Name == "" || !config.ValidDisplayName(ap.Name) || len(ap.MAC) != 64 {
			return false, nil
		}
		if _, err := hex.DecodeString(ap.MAC); err != nil {
			return false, nil
		}
		created := ap.Created
		if created.IsZero() || created.After(s.now()) {
			created = at
		}
		mc.AddAppPassword = &store.AppPassword{ID: ap.ID, Name: ap.Name, MAC: ap.MAC, CreatedAt: created}
		detail = ap.Name
	case config.EventAppPasswordRevoked:
		if !config.ValidAppPasswordID(ch.AppPasswordID) {
			return false, nil
		}
		mc.RevokeAppPassword = ch.AppPasswordID
	default:
		return false, nil
	}
	r, err := s.store.ApplyMailboxChange(ctx, e.StorageID, mc, config.MaxAppPasswords)
	if err != nil || !r.Applied {
		return false, err
	}
	if r.Revoked != nil {
		detail = r.Revoked.Name
	}
	if r.Overridden {
		detail = strings.TrimSpace(detail + " (not taken: changed in the panel at the same time)")
	}
	if r.TooManyAppPasswords {
		detail = strings.TrimSpace(detail + " (not taken: the mailbox has the most app passwords it may have)")
	}
	addr := r.Mailbox.LocalPart + "@" + r.Mailbox.Domain
	s.record(ctx, Actor{Name: addr}, "mail."+e.Kind, addr, detail)
	return true, nil
}
