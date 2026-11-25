package panel

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/danielgtmn/ownpanel/internal/dns"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// E-mail (wiki/E-Mail-Architecture.md): mail domains belong to a person;
// their mailboxes and aliases belong to the domain. The mail service
// (ownpaneld mail) gets the whole configuration pushed over its admin
// socket, like Caddy.
//
// Who may do what:
//   - tokens limited to sites never use e-mail (controlledDomain alone
//     would let them through, so the mail operations refuse them first);
//   - members see and manage the mail domains they own; admins all;
//   - read-only tokens see what their owner sees;
//   - enabling mail for a domain needs control of it (a site or domain
//     redirect, or a zone of one's own DNS connection); admins any domain;
//   - quota and send limits: owners may only lower them, admins anything
//     within bounds; only admins lift a sending block.
//
// Mailbox passwords can be set by anyone who may change the mailbox: with
// a password sign-in or an unrestricted write token, which is what the
// mail write rule already demands. The MCP server offers no tool that
// takes a mailbox password, so passwords never pass through an AI agent.

// MailService is what the panel needs from the mail service's admin socket;
// *config.Client satisfies it.
type MailService interface {
	Load(ctx context.Context, cfg *config.Config) error
	Status(ctx context.Context) (config.Status, error)
	Events(ctx context.Context, after int64) ([]config.Event, error)
	Unblock(ctx context.Context, storageID string) error
	DeleteMailbox(ctx context.Context, storageID string) error
	CheckOutbound(ctx context.Context) (config.OutboundCheck, error)
	// Send hands a finished message (a password reset link) to the
	// service, which signs it and delivers or queues it.
	Send(ctx context.Context, m config.OutgoingMail) error
}

// Limits of e-mail.
const (
	// MaxMailDomainsPerUser and MaxMailboxesPerUser bound what one person
	// owns; admins are exempt.
	MaxMailDomainsPerUser = 20
	MaxMailboxesPerUser   = 200
	// DefaultQuotaBytes is the quota of a new mailbox.
	DefaultQuotaBytes int64 = 5 << 30
	minQuotaBytes     int64 = 1 << 20
	maxQuotaBytes     int64 = 1 << 40
	maxSendPerHour          = 10_000
	maxSendPerDay           = 100_000

	// mailCheckEvery is how often the background loop looks at the MX
	// record of an inactive domain; mailCheckMin how often a request may.
	mailCheckEvery = 5 * time.Minute
	mailCheckMin   = 5 * time.Second
	// mailTimeout bounds one exchange with the mail service.
	mailTimeout = 30 * time.Second
	// mailStatusTimeout bounds asking for usage while listing mailboxes.
	mailStatusTimeout = 3 * time.Second
	// dkimPurpose binds sealed DKIM keys to their use.
	dkimPurpose = "ownpanel dkim key"
)

// mailActor names the mail service in the activity log.
var mailActor = Actor{Name: "mail", Admin: true, Via: ViaSystem}

// ---- permissions ----

// needMailRead: seeing mail domains, mailboxes and aliases.
func needMailRead(a Actor) error {
	if err := needAuth(a); err != nil {
		return err
	}
	if a.Sites != nil {
		return errorf(Forbidden, "this access token is limited to certain sites and cannot manage e-mail")
	}
	return nil
}

// needMailWrite: changing them, which only people do.
func needMailWrite(a Actor) error {
	if err := needMailRead(a); err != nil {
		return err
	}
	if err := needWrite(a); err != nil {
		return err
	}
	if a.Via == ViaSystem || a.UserID == 0 {
		return errorf(Forbidden, "e-mail must be managed by a person")
	}
	return nil
}

func (a Actor) canSeeMail(ownerID int64) bool {
	return a.authenticated() && a.Sites == nil && (a.Admin || (a.UserID != 0 && ownerID == a.UserID))
}

// visibleMailDomain returns a mail domain the actor may see; others answer
// NotFound like missing ones.
func (s *Service) visibleMailDomain(ctx context.Context, a Actor, domain string) (store.MailDomain, error) {
	if err := needMailRead(a); err != nil {
		return store.MailDomain{}, err
	}
	d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	md, err := s.store.MailDomain(ctx, d)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !a.canSeeMail(md.OwnerID)) {
		return store.MailDomain{}, errorf(NotFound, "e-mail is not enabled for %q", d)
	}
	if err != nil {
		return store.MailDomain{}, storeError(err)
	}
	return md, nil
}

// ---- views ----

// MailDomain is what callers see of a mail domain; never its keys.
type MailDomain struct {
	Domain string `json:"domain"`
	Owner  string `json:"owner"`
	// Active: the MX record pointed to the mail host; only then is mail
	// received for the domain and may its mailboxes send.
	Active      bool       `json:"active"`
	ActivatedAt *time.Time `json:"activated_at"`
	// CheckedAt and Problem tell when the MX record was last looked at
	// while inactive, and why the domain is not active yet.
	CheckedAt *time.Time `json:"checked_at"`
	Problem   string     `json:"problem"`
	Mailboxes int        `json:"mailboxes"`
	Aliases   int        `json:"aliases"`
	// DKIMSelector and DKIMRecord are the public DKIM record to publish
	// at <selector>._domainkey.<domain>.
	DKIMSelector string    `json:"dkim_selector"`
	DKIMRecord   string    `json:"dkim_record"`
	CreatedAt    time.Time `json:"created_at"`
}

func (s *Service) mailDomainView(ctx context.Context, d store.MailDomain) (MailDomain, error) {
	v := MailDomain{
		Domain: d.Domain, Owner: d.Owner, Active: d.Active, ActivatedAt: d.ActivatedAt, CheckedAt: d.CheckedAt,
		Problem: d.CheckError, Mailboxes: d.Mailboxes, Aliases: d.Aliases, CreatedAt: d.CreatedAt,
	}
	if d.Active {
		v.Problem = ""
	}
	keys, err := s.store.DKIMKeys(ctx, d.Domain)
	if err != nil {
		return MailDomain{}, storeError(err)
	}
	for _, k := range keys {
		if k.State == store.DKIMActive {
			v.DKIMSelector, v.DKIMRecord = k.Selector, "v=DKIM1; k=rsa; p="+k.PublicKey
		}
	}
	return v, nil
}

// ---- domains ----

// MailDomains lists the mail domains the actor may see, sorted by name.
func (s *Service) MailDomains(ctx context.Context, a Actor) ([]MailDomain, error) {
	if err := needMailRead(a); err != nil {
		return nil, err
	}
	all, err := s.store.MailDomains(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	out := []MailDomain{}
	for _, d := range all {
		if !a.canSeeMail(d.OwnerID) {
			continue
		}
		v, err := s.mailDomainView(ctx, d)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// MailDomainOf returns one mail domain.
func (s *Service) MailDomainOf(ctx context.Context, a Actor, domain string) (MailDomain, error) {
	d, err := s.visibleMailDomain(ctx, a, domain)
	if err != nil {
		return MailDomain{}, err
	}
	return s.mailDomainView(ctx, d)
}

// mayUseMailDomain checks that a member may enable mail for d: it is a
// domain they control and nobody else's. Admins may use any domain.
func (s *Service) mayUseMailDomain(ctx context.Context, a Actor, d string) error {
	if a.Admin {
		return nil
	}
	sites, err := s.store.Sites(ctx)
	if err != nil {
		return storeError(err)
	}
	for _, site := range sites {
		if slices.Contains(site.Domains, d) && !a.canSee(site) {
			return errorf(Invalid, "the domain %s is already used by another person", d)
		}
	}
	redirects, err := s.store.Redirects(ctx)
	if err != nil {
		return storeError(err)
	}
	for _, r := range redirects {
		if r.SourcePath == "" && r.SourceDomain == d && !a.canSeeRedirect(r, nil) {
			return errorf(Invalid, "the domain %s is already used by another person", d)
		}
	}
	if _, err := s.controlledDomain(ctx, a, d); err == nil {
		return nil
	} else if KindOf(err) != NotFound {
		return err
	}
	conns, err := s.ownConnections(ctx, a)
	if err != nil {
		return err
	}
	if matches, _ := s.zoneMatches(ctx, conns, d); len(matches) > 0 {
		return nil
	}
	return errorf(Forbidden, "you can enable e-mail only for a domain of one of your sites or redirects, or for one in a zone of your DNS connections; %s is neither", d)
}

// mailDomainName normalizes a domain for e-mail.
func (s *Service) mailDomainName(domain string) (string, error) {
	d, err := store.NormalizeDomain(domain)
	if err != nil || !config.ValidDomain(d) {
		return "", errorf(Invalid, "%q is not a valid domain name; enter one like example.com", strings.TrimSpace(domain))
	}
	labels := strings.Split(d, ".")
	if strings.Trim(labels[len(labels)-1], "0123456789") == "" {
		return "", errorf(Invalid, "e-mail needs a domain name like example.com, not an IP address")
	}
	if h := s.MailHost(); h != "" && d == h {
		// postmaster@ and abuse@ of the mail host go to the postmaster
		// address; mailboxes there would be confusing.
		return "", errorf(Invalid, "%s is the name of the mail server itself and cannot be a mail domain", d)
	}
	return d, nil
}

// newDKIMKey makes an RSA-2048 key and returns it sealed (PKCS#8 PEM) with
// its public key (base64 DER) for the DNS record.
func (s *Service) newDKIMKey() (store.DKIMKey, error) {
	if s.secrets == nil {
		return store.DKIMKey{}, errorf(Internal, "no secret key configured; DKIM keys cannot be stored")
	}
	k, err := newRSAKey()
	if err != nil {
		return store.DKIMKey{}, newError(Internal, err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return store.DKIMKey{}, newError(Internal, err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		return store.DKIMKey{}, newError(Internal, err)
	}
	priv := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	return store.DKIMKey{
		Selector:   "op" + s.now().UTC().Format("200601"),
		PrivateKey: s.secrets.Seal(priv, dkimPurpose),
		PublicKey:  base64.StdEncoding.EncodeToString(pub),
		State:      store.DKIMActive,
	}, nil
}

// EnableMailDomain turns on e-mail for a domain, owned by the actor. The
// domain becomes active (receives mail) once its MX record points to the
// mail host; that is checked right away and then in the background.
func (s *Service) EnableMailDomain(ctx context.Context, a Actor, domain string) (MailDomain, error) {
	if err := needMailWrite(a); err != nil {
		return MailDomain{}, err
	}
	d, err := s.mailDomainName(domain)
	if err != nil {
		return MailDomain{}, err
	}
	if err := s.mayUseMailDomain(ctx, a, d); err != nil {
		return MailDomain{}, err
	}
	key, err := s.newDKIMKey()
	if err != nil {
		return MailDomain{}, err
	}
	limit := MaxMailDomainsPerUser
	if a.Admin {
		limit = 0
	}
	md, err := s.store.CreateMailDomain(ctx, d, a.UserID, key, limit)
	if err != nil {
		return MailDomain{}, storeError(err)
	}
	s.record(ctx, a, "mail.domain.enable", d, "DKIM selector "+key.Selector)
	md, err = s.checkMailDomain(ctx, md)
	if err != nil {
		return MailDomain{}, err
	}
	v, err := s.mailDomainView(ctx, md)
	if err != nil {
		return MailDomain{}, err
	}
	if md.Active {
		return v, s.mailSaved(ctx)
	}
	return v, nil
}

// CheckMailDomain looks at the domain's MX record now and activates the
// domain if it points to the mail host.
func (s *Service) CheckMailDomain(ctx context.Context, a Actor, domain string) (MailDomain, error) {
	md, err := s.visibleMailDomain(ctx, a, domain)
	if err != nil {
		return MailDomain{}, err
	}
	wasActive := md.Active
	if !md.Active && (md.CheckedAt == nil || s.now().Sub(*md.CheckedAt) >= mailCheckMin || s.now().Before(*md.CheckedAt)) {
		if md, err = s.checkMailDomain(ctx, md); err != nil {
			return MailDomain{}, err
		}
	}
	v, err := s.mailDomainView(ctx, md)
	if err != nil {
		return MailDomain{}, err
	}
	if md.Active && !wasActive {
		return v, s.mailSaved(ctx)
	}
	return v, nil
}

// ActivateMailDomain makes a domain receive mail here without waiting for
// its MX record: for setups the check cannot see (a gateway in front of the
// server, split DNS, tests). Only admins may: they run the server anyway, so
// it gives them nothing they could not do otherwise, while a member could
// use it to catch mail other mailboxes of this server send to a domain they
// do not own.
func (s *Service) ActivateMailDomain(ctx context.Context, a Actor, domain string) (MailDomain, error) {
	md, err := s.visibleMailDomain(ctx, a, domain)
	if err != nil {
		return MailDomain{}, err
	}
	if err := needMailWrite(a); err != nil {
		return MailDomain{}, err
	}
	if !a.administers() {
		return MailDomain{}, errorf(Forbidden, "only administrators can turn on receiving before the domain's MX record points here")
	}
	if !md.Active {
		now := s.now()
		if err := s.store.RecordMailDomainCheck(ctx, md.Domain, now, true, ""); err != nil {
			return MailDomain{}, storeError(err)
		}
		md.Active, md.ActivatedAt, md.CheckedAt, md.CheckError = true, &now, &now, ""
		s.record(ctx, a, "mail.domain.activate-manually", md.Domain, "")
	}
	v, err := s.mailDomainView(ctx, md)
	if err != nil {
		return MailDomain{}, err
	}
	return v, s.mailSaved(ctx)
}

// DeleteMailDomain removes e-mail for a domain with all its mailboxes and
// aliases; the mail service deletes the stored mail. confirm must repeat
// the domain.
func (s *Service) DeleteMailDomain(ctx context.Context, a Actor, domain, confirm string) error {
	md, err := s.visibleMailDomain(ctx, a, domain)
	if err != nil {
		return err
	}
	if err := needMailWrite(a); err != nil {
		return err
	}
	if strings.ToLower(strings.TrimSpace(confirm)) != md.Domain {
		return errorf(Invalid, "to delete e-mail for %s with all its mailboxes and stored mail, repeat the domain name exactly", md.Domain)
	}
	if _, err := s.store.DeleteMailDomain(ctx, md.Domain); err != nil {
		return storeError(err)
	}
	s.record(ctx, a, "mail.domain.delete", md.Domain, fmt.Sprintf("%d mailboxes, %d aliases", md.Mailboxes, md.Aliases))
	return s.mailSaved(ctx)
}

// ---- activation ----

// checkMailDomain looks up an inactive domain's MX record at its
// authoritative nameservers and records the outcome. A domain whose MX
// points to the mail host becomes active.
func (s *Service) checkMailDomain(ctx context.Context, md store.MailDomain) (store.MailDomain, error) {
	if md.Active {
		return md, nil
	}
	active, problem := s.mxPointsHere(ctx, md.Domain)
	if ctx.Err() != nil {
		return md, newError(Internal, ctx.Err())
	}
	now := s.now()
	if err := s.store.RecordMailDomainCheck(context.WithoutCancel(ctx), md.Domain, now, active, problem); err != nil {
		return md, storeError(err)
	}
	md.CheckedAt, md.CheckError = &now, problem
	if active {
		md.Active, md.ActivatedAt, md.CheckError = true, &now, ""
		s.record(ctx, System, "mail.domain.activate", md.Domain, "MX points to "+s.MailHost())
	}
	return md, nil
}

// mxPointsHere tells whether a domain's MX record names the mail host, and
// if not, why.
func (s *Service) mxPointsHere(ctx context.Context, domain string) (bool, string) {
	host := s.MailHost()
	if host == "" {
		return false, "The name of the mail server is not set yet; an admin sets it in the settings."
	}
	lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	mx, err := s.mx.LookupMX(lctx, domain)
	var de *net.DNSError
	switch {
	case errors.As(err, &de) && de.IsNotFound:
		return false, fmt.Sprintf("The domain has no MX record yet. Add one that points to %s.", host)
	case err != nil:
		return false, "The MX record could not be looked up; it is tried again in a few minutes."
	}
	var found []string
	for _, m := range mx {
		h := strings.ToLower(strings.TrimSuffix(m.Host, "."))
		if h == host {
			return true, ""
		}
		if h == "" {
			return false, "The domain's MX record says it receives no e-mail (null MX). Replace it with one that points to " + host + "."
		}
		found = append(found, h)
	}
	return false, fmt.Sprintf("The MX record points to %s, not to %s.", strings.Join(found, ", "), host)
}

// activateMailDomains checks the inactive domains that were not looked at
// for mailCheckEvery and reports whether one became active.
func (s *Service) activateMailDomains(ctx context.Context) bool {
	all, err := s.store.MailDomains(ctx)
	if err != nil {
		slog.Error("listing mail domains failed", "err", err)
		return false
	}
	activated := false
	now := s.now()
	for _, d := range all {
		if d.Active || (d.CheckedAt != nil && now.Sub(*d.CheckedAt) < mailCheckEvery && !now.Before(*d.CheckedAt)) {
			continue
		}
		if ctx.Err() != nil {
			return activated
		}
		md, err := s.checkMailDomain(ctx, d)
		if err != nil {
			slog.Warn("checking a mail domain failed", "domain", d.Domain, "err", err)
			continue
		}
		activated = activated || md.Active
	}
	return activated
}

// noMX answers every MX lookup with a failure; used when only an address
// resolver was configured (tests), so nothing goes to the network.
type noMX struct{}

func (noMX) LookupMX(context.Context, string) ([]*net.MX, error) {
	return nil, errors.New("no MX resolver configured")
}

var (
	_ dns.MXResolver = noMX{}
	_ MailService    = (*config.Client)(nil)
)

// newRSAKey makes a DKIM key; tests replace it to save time.
var newRSAKey = func() (*rsa.PrivateKey, error) { return rsa.GenerateKey(rand.Reader, 2048) }
