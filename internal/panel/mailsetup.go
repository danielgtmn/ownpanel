package panel

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/dns"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mailsetup"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// Automatic setup of mail programs (wiki/E-Mail-Architecture.md, "Mailboxes
// and clients"). Mail programs find their settings by themselves:
//
//   - Thunderbird (and others) at https://autoconfig.<domain>/mail/config-v1.1.xml
//     and https://<domain>/.well-known/autoconfig/mail/config-v1.1.xml;
//   - Outlook by POST to https://autodiscover.<domain>/autodiscover/autodiscover.xml;
//   - Apple devices from a profile the owner downloads for a mailbox.
//
// The public answers exist only for active mail domains and say nothing a
// DNS lookup would not (the mail host and the ports); never whether a
// mailbox exists. The web server serves autoconfig.<domain> and
// autodiscover.<domain> only once the panel saw the name point here (a
// CNAME to the mail host, or A/AAAA records with the server's addresses),
// so it never asks for certificates of names that lead elsewhere. The
// well-known path is served on the domains of sites that are also active
// mail domains.

// Names below a mail domain that mail programs look up.
const (
	autoconfigLabel   = "autoconfig"
	autodiscoverLabel = "autodiscover"
)

// mailSetupWorkers bounds the lookups of the background check.
const mailSetupWorkers = 8

// setupHostsState remembers which names for setting up mail programs point
// to this server; the zero value is ready.
type setupHostsState struct {
	mu   sync.Mutex
	here map[string]bool
	// next is when the background loop looks again; zero means now.
	next time.Time
}

// note records what a lookup of name found and tells whether that changed
// whether it points here. A failed lookup changes nothing.
func (h *setupHostsState) note(name string, st dns.State) bool {
	if st != dns.PointsHere && st != dns.PointsElsewhere && st != dns.NoRecord {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	here := st == dns.PointsHere
	if h.here[name] == here {
		return false
	}
	if h.here == nil {
		h.here = map[string]bool{}
	}
	if here {
		h.here[name] = true
	} else {
		delete(h.here, name)
	}
	return true
}

// checkSoon makes the background loop look at the names on its next round.
func (h *setupHostsState) checkSoon() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next = time.Time{}
}

// due tells whether the background loop should look now, and if so plans
// the round after.
func (h *setupHostsState) due(now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.next.IsZero() && now.Before(h.next) && h.next.Sub(now) <= mailCheckEvery {
		return false
	}
	h.next = now.Add(mailCheckEvery)
	return true
}

func (h *setupHostsState) pointsHere(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.here[name]
}

// setupNames are the names below domain that mail programs look up.
func setupNames(domain string) []string {
	return []string{autoconfigLabel + "." + domain, autodiscoverLabel + "." + domain}
}

// checkMailSetupHosts looks up where the names for setting up mail
// programs of every active mail domain point (at most every
// mailCheckEvery) and tells whether that changed for one of them.
func (s *Service) checkMailSetupHosts(ctx context.Context) bool {
	if !s.setupHosts.due(s.now()) {
		return false
	}
	ips := s.effectiveServerIPs()
	host := s.MailHost()
	all, err := s.store.MailDomains(ctx)
	if err != nil {
		slog.Error("listing mail domains failed", "err", err)
		return false
	}
	var names []string
	for _, d := range all {
		if d.Active && d.Domain != host {
			names = append(names, setupNames(d.Domain)...)
		}
	}
	changed := false
	// Names of domains that are gone or inactive, and every name when the
	// server's addresses are not known, no longer point here.
	s.setupHosts.mu.Lock()
	for name := range s.setupHosts.here {
		if len(ips) == 0 || !slices.Contains(names, name) {
			delete(s.setupHosts.here, name)
			changed = true
		}
	}
	s.setupHosts.mu.Unlock()
	if len(ips) == 0 || host == "" {
		return changed
	}
	work := make(chan string)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range min(mailSetupWorkers, len(names)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for name := range work {
				lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
				c := dns.CheckDomain(lctx, s.resolver, name, ips)
				cancel()
				if s.setupHosts.note(name, c.State) {
					mu.Lock()
					changed = true
					mu.Unlock()
				}
			}
		}()
	}
	for _, name := range names {
		if ctx.Err() != nil {
			break
		}
		work <- name
	}
	close(work)
	wg.Wait()
	return changed
}

// mailSetupCaddy fills in the web server options for setting up mail
// programs: the names that point here, and the active mail domains whose
// sites answer the well-known path. A name that is the panel's or the mail
// host stays theirs; a name that is a site's or a domain redirect's stays
// theirs only when it belongs to the owner of the mail domain. Anybody
// else's claim on it does not take the setup away from the mail domain's
// owner (Build puts the setup route ahead of sites and redirects).
func (s *Service) mailSetupCaddy(ctx context.Context, o *caddy.Options, sites []store.Site) error {
	if o.PanelUpstream == "" {
		return nil
	}
	host := s.MailHost()
	if host == "" {
		return nil
	}
	all, err := s.store.MailDomains(ctx)
	if err != nil {
		return err
	}
	redirects, err := s.store.Redirects(ctx)
	if err != nil {
		return err
	}
	reserved := map[string]bool{host: true, o.PanelDomain: true}
	claimedBy := map[string]int64{} // name -> owner of the site or domain redirect
	for _, site := range sites {
		for _, d := range site.Domains {
			claimedBy[d] = site.OwnerID
		}
	}
	for _, r := range redirects {
		if r.SourcePath == "" {
			claimedBy[r.SourceDomain] = r.OwnerID
		}
	}
	for _, d := range all {
		if !d.Active || d.Domain == host {
			continue
		}
		o.MailAutoconfigDomains = append(o.MailAutoconfigDomains, d.Domain)
		for _, name := range setupNames(d.Domain) {
			if reserved[name] {
				continue
			}
			if owner, ok := claimedBy[name]; ok && owner == d.OwnerID {
				continue
			}
			if s.setupHosts.pointsHere(name) {
				o.MailSetupHosts = append(o.MailSetupHosts, name)
			}
		}
	}
	return nil
}

// MailClientSetup is what a mail program needs to set itself up for an
// address of Domain.
type MailClientSetup struct {
	Domain string             `json:"domain"`
	Client MailClientSettings `json:"client"`
}

// clientSettings are the settings for programs; calendars and contacts
// only when the mail host serves them (mailWeb).
func (s *Service) clientSettings(host string) MailClientSettings {
	c := mailsetup.Settings(host)
	if host == s.MailHost() && s.selfServiceProblem() == "" {
		c = c.WithDAV()
	}
	return c
}

// errNoMailSetup is the answer of the public setup for everything that is
// not an active mail domain here.
var errNoMailSetup = errorf(NotFound, "no settings for mail programs here")

// MailSetupFor returns the settings for mail programs of an active mail
// domain. It needs no sign-in: mail programs ask before they have a
// password. Everything else (unknown or inactive domains, no mail host)
// answers NotFound alike, and nothing depends on a mailbox.
func (s *Service) MailSetupFor(ctx context.Context, domain string) (MailClientSetup, error) {
	d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	host := s.MailHost()
	if host == "" || d == host || !config.ValidDomain(d) {
		return MailClientSetup{}, errNoMailSetup
	}
	md, err := s.store.MailDomain(ctx, d)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !md.Active) {
		return MailClientSetup{}, errNoMailSetup
	}
	if err != nil {
		return MailClientSetup{}, storeError(err)
	}
	return MailClientSetup{Domain: md.Domain, Client: s.clientSettings(host)}, nil
}

// MailboxSetup is what a mail program needs for one mailbox.
type MailboxSetup struct {
	Address string             `json:"address"`
	Client  MailClientSettings `json:"client"`
}

// MailboxSetupOf returns the settings for mail programs of a mailbox
// (whoever may see the mailbox; no password, nothing secret).
func (s *Service) MailboxSetupOf(ctx context.Context, a Actor, id int64) (MailboxSetup, error) {
	m, err := s.visibleMailbox(ctx, a, id)
	if err != nil {
		return MailboxSetup{}, err
	}
	host, err := s.mailHostOrConflict()
	if err != nil {
		return MailboxSetup{}, err
	}
	return MailboxSetup{Address: m.LocalPart + "@" + m.Domain, Client: s.clientSettings(host)}, nil
}
