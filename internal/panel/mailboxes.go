package panel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// Mailbox is what callers see of a mailbox; never its password hash.
type Mailbox struct {
	ID          int64     `json:"id"`
	Address     string    `json:"address"`
	Domain      string    `json:"domain"`
	LocalPart   string    `json:"local_part"`
	Owner       string    `json:"owner"`
	QuotaBytes  int64     `json:"quota_bytes"`
	SendPerHour int       `json:"send_per_hour"`
	SendPerDay  int       `json:"send_per_day"`
	CreatedAt   time.Time `json:"created_at"`
	// DisplayName is shown as the sender's name; may be empty.
	DisplayName string `json:"display_name"`
	// AppPasswordsOnly: mail programs sign in only with app passwords.
	AppPasswordsOnly bool `json:"app_passwords_only"`
	// Usage is what the mail service reports; nil when it could not be
	// asked (or is not set up).
	Usage *MailboxUsage `json:"usage"`
}

// MailboxUsage is the stored mail and the sending block of a mailbox, as
// reported by the mail service.
type MailboxUsage struct {
	UsedBytes     int64      `json:"used_bytes"`
	Messages      int        `json:"messages"`
	Blocked       bool       `json:"blocked"`
	BlockedReason string     `json:"blocked_reason"`
	BlockedAt     *time.Time `json:"blocked_at"`
}

// MailboxInput is a mailbox to create. Nil limits mean the defaults.
type MailboxInput struct {
	LocalPart   string
	Password    string
	DisplayName string
	QuotaBytes  *int64
	SendPerHour *int
	SendPerDay  *int
}

// MailboxLimits changes a mailbox's quota and send limits; nil fields stay.
type MailboxLimits struct {
	QuotaBytes  *int64
	SendPerHour *int
	SendPerDay  *int
}

// MailAlias is what callers see of an alias.
type MailAlias struct {
	ID        int64     `json:"id"`
	Address   string    `json:"address"`
	Domain    string    `json:"domain"`
	LocalPart string    `json:"local_part"`
	CatchAll  bool      `json:"catch_all"`
	Targets   []string  `json:"targets"`
	Owner     string    `json:"owner"`
	CreatedAt time.Time `json:"created_at"`
}

func mailboxView(m store.Mailbox, usage map[string]config.MailboxStatus) Mailbox {
	v := Mailbox{
		ID: m.ID, Address: m.LocalPart + "@" + m.Domain, Domain: m.Domain, LocalPart: m.LocalPart, Owner: m.Owner,
		QuotaBytes: m.QuotaBytes, SendPerHour: m.SendPerHour, SendPerDay: m.SendPerDay, CreatedAt: m.CreatedAt,
		DisplayName: m.DisplayName, AppPasswordsOnly: m.AppPasswordsOnly,
	}
	if usage != nil {
		u := MailboxUsage{}
		if st, ok := usage[m.StorageID]; ok {
			u = MailboxUsage{UsedBytes: st.UsedBytes, Messages: st.Messages, Blocked: st.Blocked, BlockedReason: st.BlockedReason}
			if !st.BlockedAt.IsZero() {
				t := st.BlockedAt
				u.BlockedAt = &t
			}
		}
		v.Usage = &u
	}
	return v
}

func aliasView(a store.MailAlias) MailAlias {
	return MailAlias{
		ID: a.ID, Address: a.LocalPart + "@" + a.Domain, Domain: a.Domain, LocalPart: a.LocalPart, CatchAll: a.LocalPart == "*",
		Targets: slices.Clone(a.Targets), Owner: a.Owner, CreatedAt: a.CreatedAt,
	}
}

// mailUsage asks the mail service for the usage of every mailbox, by
// storage id; nil if it cannot be asked quickly.
func (s *Service) mailUsage(ctx context.Context) map[string]config.MailboxStatus {
	if s.mail == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, mailStatusTimeout)
	defer cancel()
	st, err := s.mail.Status(ctx)
	if err != nil {
		return nil
	}
	out := make(map[string]config.MailboxStatus, len(st.Mailboxes))
	for _, m := range st.Mailboxes {
		out[m.StorageID] = m
	}
	return out
}

// visibleMailbox returns a mailbox the actor may see; others answer
// NotFound like missing ones.
func (s *Service) visibleMailbox(ctx context.Context, a Actor, id int64) (store.Mailbox, error) {
	if err := needMailRead(a); err != nil {
		return store.Mailbox{}, err
	}
	m, err := s.store.Mailbox(ctx, id)
	if err != nil {
		return store.Mailbox{}, storeError(err)
	}
	if !a.canSeeMail(m.OwnerID) {
		return store.Mailbox{}, errorf(NotFound, "mailbox not found")
	}
	return m, nil
}

// mailDomainActive tells whether changes to the domain's mailboxes and
// aliases must be pushed; those of inactive domains are not in the
// configuration.
func (s *Service) mailDomainActive(ctx context.Context, domain string) bool {
	d, err := s.store.MailDomain(ctx, domain)
	return err != nil || d.Active // when in doubt, push
}

// mailChanged pushes after a change of a mailbox or alias of domain.
func (s *Service) mailChanged(ctx context.Context, domain string) error {
	if !s.mailDomainActive(ctx, domain) {
		return nil
	}
	return s.mailSaved(ctx)
}

// ---- mailboxes ----

// Mailboxes lists the mailboxes of a mail domain in the order they were
// created ("" domain: of every domain the actor may see).
func (s *Service) Mailboxes(ctx context.Context, a Actor, domain string) ([]Mailbox, error) {
	if err := needMailRead(a); err != nil {
		return nil, err
	}
	d := ""
	if strings.TrimSpace(domain) != "" {
		md, err := s.visibleMailDomain(ctx, a, domain)
		if err != nil {
			return nil, err
		}
		d = md.Domain
	}
	list, err := s.store.Mailboxes(ctx, d)
	if err != nil {
		return nil, storeError(err)
	}
	usage := s.mailUsage(ctx)
	out := []Mailbox{}
	for _, m := range list {
		if a.canSeeMail(m.OwnerID) {
			out = append(out, mailboxView(m, usage))
		}
	}
	return out, nil
}

// MailboxOf returns one mailbox.
func (s *Service) MailboxOf(ctx context.Context, a Actor, id int64) (Mailbox, error) {
	m, err := s.visibleMailbox(ctx, a, id)
	if err != nil {
		return Mailbox{}, err
	}
	return mailboxView(m, s.mailUsage(ctx)), nil
}

// newStorageID is 128 random bits in hex.
func newStorageID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// checkLimits applies a change of limits to cur. Admins may set anything
// within bounds (quota 0: no limit); others may only lower values.
func checkLimits(a Actor, cur store.Mailbox, quota *int64, perHour, perDay *int) (store.Mailbox, error) {
	next := cur
	if quota != nil {
		q := *quota
		switch {
		case q == 0 && a.Admin:
		case q < minQuotaBytes || q > maxQuotaBytes:
			return cur, errorf(Invalid, "the quota must be between 1 MiB and 1 TiB")
		case !a.Admin && (cur.QuotaBytes != 0 && q > cur.QuotaBytes):
			return cur, errorf(Forbidden, "only admins can raise the quota (now %d bytes)", cur.QuotaBytes)
		}
		next.QuotaBytes = q
	}
	if perHour != nil {
		switch v := *perHour; {
		case v < 1 || v > maxSendPerHour:
			return cur, errorf(Invalid, "the messages per hour must be between 1 and %d", maxSendPerHour)
		case !a.Admin && v > cur.SendPerHour:
			return cur, errorf(Forbidden, "only admins can raise the messages per hour (now %d)", cur.SendPerHour)
		}
		next.SendPerHour = *perHour
	}
	if perDay != nil {
		switch v := *perDay; {
		case v < 1 || v > maxSendPerDay:
			return cur, errorf(Invalid, "the messages per day must be between 1 and %d", maxSendPerDay)
		case !a.Admin && v > cur.SendPerDay:
			return cur, errorf(Forbidden, "only admins can raise the messages per day (now %d)", cur.SendPerDay)
		}
		next.SendPerDay = *perDay
	}
	return next, nil
}

func limitsDetail(m store.Mailbox) string {
	q := "no quota"
	if m.QuotaBytes > 0 {
		q = fmt.Sprintf("quota %d MiB", m.QuotaBytes>>20)
	}
	return fmt.Sprintf("%s, %d messages per hour, %d per day", q, m.SendPerHour, m.SendPerDay)
}

// localPart normalizes the local part of a mailbox or (with catchAll) an
// alias.
func localPart(v string, catchAll bool) (string, error) {
	l := strings.ToLower(strings.TrimSpace(v))
	if l == "*" {
		if catchAll {
			return l, nil
		}
		return "", errorf(Invalid, "* stands for every address (the catch-all) and is only possible for an alias")
	}
	if !config.ValidLocalPart(l) {
		return "", errorf(Invalid, "%q is not a valid name for an address: use letters, digits and . - _ + (no spaces, at most 64 characters)", strings.TrimSpace(v))
	}
	return l, nil
}

// CreateMailbox adds a mailbox to a mail domain the actor may change.
func (s *Service) CreateMailbox(ctx context.Context, a Actor, domain string, in MailboxInput) (Mailbox, error) {
	md, err := s.visibleMailDomain(ctx, a, domain)
	if err != nil {
		return Mailbox{}, err
	}
	if err := needMailWrite(a); err != nil {
		return Mailbox{}, err
	}
	l, err := localPart(in.LocalPart, false)
	if err != nil {
		return Mailbox{}, err
	}
	if err := validatePassword(in.Password); err != nil {
		return Mailbox{}, err
	}
	dn, err := displayName(in.DisplayName)
	if err != nil {
		return Mailbox{}, err
	}
	m, err := checkLimits(a, store.Mailbox{
		QuotaBytes: DefaultQuotaBytes, SendPerHour: config.DefaultSendPerHour, SendPerDay: config.DefaultSendPerDay,
	}, in.QuotaBytes, in.SendPerHour, in.SendPerDay)
	if err != nil {
		return Mailbox{}, err
	}
	if m.PasswordHash, err = hashPassword(in.Password); err != nil {
		return Mailbox{}, newError(Internal, err)
	}
	m.StorageID, m.Domain, m.LocalPart, m.DisplayName = newStorageID(), md.Domain, l, dn
	limit := MaxMailboxesPerUser
	if a.Admin {
		limit = 0
	}
	saved, err := s.store.CreateMailbox(ctx, m, limit)
	if err != nil {
		return Mailbox{}, storeError(err)
	}
	s.record(ctx, a, "mail.mailbox.create", l+"@"+md.Domain, limitsDetail(saved))
	return mailboxView(saved, nil), s.mailChanged(ctx, md.Domain)
}

// SetMailboxPassword replaces a mailbox's password. Mail clients signed in
// with the old one have to sign in again.
func (s *Service) SetMailboxPassword(ctx context.Context, a Actor, id int64, password string) error {
	m, err := s.visibleMailbox(ctx, a, id)
	if err != nil {
		return err
	}
	if err := needMailWrite(a); err != nil {
		return err
	}
	if err := validatePassword(password); err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return newError(Internal, err)
	}
	if err := s.store.SetMailboxPassword(ctx, id, hash); err != nil {
		return storeError(err)
	}
	s.record(ctx, a, "mail.mailbox.password", m.LocalPart+"@"+m.Domain, "")
	return s.mailChanged(ctx, m.Domain)
}

// SetMailboxLimits changes a mailbox's quota and send limits. Only admins
// may raise them.
func (s *Service) SetMailboxLimits(ctx context.Context, a Actor, id int64, in MailboxLimits) (Mailbox, error) {
	m, err := s.visibleMailbox(ctx, a, id)
	if err != nil {
		return Mailbox{}, err
	}
	if err := needMailWrite(a); err != nil {
		return Mailbox{}, err
	}
	next, err := checkLimits(a, m, in.QuotaBytes, in.SendPerHour, in.SendPerDay)
	if err != nil {
		return Mailbox{}, err
	}
	if next == m {
		return mailboxView(m, nil), nil
	}
	if err := s.store.SetMailboxLimits(ctx, id, next.QuotaBytes, next.SendPerHour, next.SendPerDay); err != nil {
		return Mailbox{}, storeError(err)
	}
	s.record(ctx, a, "mail.mailbox.limits", m.LocalPart+"@"+m.Domain, limitsDetail(next))
	return mailboxView(next, nil), s.mailChanged(ctx, m.Domain)
}

// DeleteMailbox removes a mailbox; the mail service deletes its stored
// mail (retried later if it cannot be reached now).
func (s *Service) DeleteMailbox(ctx context.Context, a Actor, id int64) error {
	m, err := s.visibleMailbox(ctx, a, id)
	if err != nil {
		return err
	}
	if err := needMailWrite(a); err != nil {
		return err
	}
	if _, err := s.store.DeleteMailbox(ctx, id); err != nil {
		return storeError(err)
	}
	s.record(ctx, a, "mail.mailbox.delete", m.LocalPart+"@"+m.Domain, "")
	return s.mailSaved(ctx)
}

// UnblockMailbox lets a mailbox the mail service blocked send again
// (admins only).
func (s *Service) UnblockMailbox(ctx context.Context, a Actor, id int64) error {
	m, err := s.visibleMailbox(ctx, a, id)
	if err != nil {
		return err
	}
	if err := needAdmin(a); err != nil {
		return err
	}
	if s.mail == nil {
		return errorf(Conflict, "the mail service is not set up on this server")
	}
	ctx2, cancel := context.WithTimeout(ctx, mailTimeout)
	defer cancel()
	if err := s.mail.Unblock(ctx2, m.StorageID); err != nil {
		return mailServiceError(err)
	}
	s.record(ctx, a, "mail.mailbox.unblock", m.LocalPart+"@"+m.Domain, "")
	return nil
}

// mailServiceError explains a failed request to the mail service.
func mailServiceError(err error) error {
	var e *config.Error
	if errors.As(err, &e) && e.Status == 404 {
		return &Error{Kind: NotFound, Msg: "the mail server does not know this mailbox yet", Err: err}
	}
	return &Error{Kind: Unavailable, Msg: "the mail server could not be reached or reported a problem; try again later", Err: err}
}

// ---- aliases ----

// visibleAlias returns an alias the actor may see.
func (s *Service) visibleAlias(ctx context.Context, a Actor, id int64) (store.MailAlias, error) {
	if err := needMailRead(a); err != nil {
		return store.MailAlias{}, err
	}
	al, err := s.store.MailAlias(ctx, id)
	if err != nil {
		return store.MailAlias{}, storeError(err)
	}
	if !a.canSeeMail(al.OwnerID) {
		return store.MailAlias{}, errorf(NotFound, "alias not found")
	}
	return al, nil
}

// aliasTargets normalizes the targets of an alias.
func aliasTargets(in []string) ([]string, error) {
	out := []string{}
	for _, t := range in {
		v := strings.ToLower(strings.TrimSpace(t))
		if v == "" {
			continue
		}
		if !config.ValidAddress(v) {
			return nil, errorf(Invalid, "%q is not a valid e-mail address", strings.TrimSpace(t))
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, errorf(Invalid, "enter at least one address the alias delivers to")
	}
	if len(out) > config.MaxTargets {
		return nil, errorf(Invalid, "an alias can deliver to at most %d addresses", config.MaxTargets)
	}
	return out, nil
}

// MailAliases lists the aliases of a mail domain ("" domain: of every
// domain the actor may see), sorted by domain and local part.
func (s *Service) MailAliases(ctx context.Context, a Actor, domain string) ([]MailAlias, error) {
	if err := needMailRead(a); err != nil {
		return nil, err
	}
	d := ""
	if strings.TrimSpace(domain) != "" {
		md, err := s.visibleMailDomain(ctx, a, domain)
		if err != nil {
			return nil, err
		}
		d = md.Domain
	}
	list, err := s.store.MailAliases(ctx, d)
	if err != nil {
		return nil, storeError(err)
	}
	out := []MailAlias{}
	for _, al := range list {
		if a.canSeeMail(al.OwnerID) {
			out = append(out, aliasView(al))
		}
	}
	return out, nil
}

// MailAliasOf returns one alias.
func (s *Service) MailAliasOf(ctx context.Context, a Actor, id int64) (MailAlias, error) {
	al, err := s.visibleAlias(ctx, a, id)
	if err != nil {
		return MailAlias{}, err
	}
	return aliasView(al), nil
}

// CreateMailAlias adds an alias ("*" local part: the catch-all) to a mail
// domain the actor may change.
func (s *Service) CreateMailAlias(ctx context.Context, a Actor, domain, local string, targets []string) (MailAlias, error) {
	md, err := s.visibleMailDomain(ctx, a, domain)
	if err != nil {
		return MailAlias{}, err
	}
	if err := needMailWrite(a); err != nil {
		return MailAlias{}, err
	}
	l, err := localPart(local, true)
	if err != nil {
		return MailAlias{}, err
	}
	t, err := aliasTargets(targets)
	if err != nil {
		return MailAlias{}, err
	}
	saved, err := s.store.CreateMailAlias(ctx, store.MailAlias{Domain: md.Domain, LocalPart: l, Targets: t})
	if err != nil {
		return MailAlias{}, storeError(err)
	}
	s.record(ctx, a, "mail.alias.create", l+"@"+md.Domain, aliasDetail(t))
	return aliasView(saved), s.mailChanged(ctx, md.Domain)
}

// UpdateMailAlias replaces an alias's local part and targets.
func (s *Service) UpdateMailAlias(ctx context.Context, a Actor, id int64, local string, targets []string) (MailAlias, error) {
	cur, err := s.visibleAlias(ctx, a, id)
	if err != nil {
		return MailAlias{}, err
	}
	if err := needMailWrite(a); err != nil {
		return MailAlias{}, err
	}
	l, err := localPart(local, true)
	if err != nil {
		return MailAlias{}, err
	}
	t, err := aliasTargets(targets)
	if err != nil {
		return MailAlias{}, err
	}
	saved, err := s.store.UpdateMailAlias(ctx, store.MailAlias{ID: id, LocalPart: l, Targets: t})
	if err != nil {
		return MailAlias{}, storeError(err)
	}
	detail := aliasDetail(t)
	if cur.LocalPart != l {
		detail = "was " + cur.LocalPart + "@" + cur.Domain + "; " + detail
	}
	s.record(ctx, a, "mail.alias.update", l+"@"+cur.Domain, detail)
	return aliasView(saved), s.mailChanged(ctx, cur.Domain)
}

// DeleteMailAlias removes an alias.
func (s *Service) DeleteMailAlias(ctx context.Context, a Actor, id int64) error {
	cur, err := s.visibleAlias(ctx, a, id)
	if err != nil {
		return err
	}
	if err := needMailWrite(a); err != nil {
		return err
	}
	if err := s.store.DeleteMailAlias(ctx, id); err != nil {
		return storeError(err)
	}
	s.record(ctx, a, "mail.alias.delete", cur.LocalPart+"@"+cur.Domain, aliasDetail(cur.Targets))
	return s.mailChanged(ctx, cur.Domain)
}

func aliasDetail(targets []string) string {
	d := "to " + strings.Join(targets, ", ")
	if len(d) > 500 {
		d = d[:497] + "…"
	}
	return d
}
