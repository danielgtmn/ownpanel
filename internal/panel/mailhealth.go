package panel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/danielgtmn/ownpanel/internal/dns"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// Mail health (wiki/E-Mail-Architecture.md, "Deliverability"): what an
// admin needs to know before mail from this server is accepted elsewhere.
// Every check answers within healthTimeout; a lookup that fails is
// "unknown", never an error of the whole request.

// Health check ids.
const (
	HealthService      = "service"
	HealthMailHostDNS  = "mail_host_dns"
	HealthPTR          = "ptr"
	HealthOutboundPort = "outbound_port"
	HealthCertificate  = "certificate"
	HealthListeners    = "listeners"
	HealthPostmaster   = "postmaster"
	HealthBlocklists   = "blocklists"
)

// Health check states.
const (
	HealthOK      = "ok"
	HealthWarning = "warning"
	HealthError   = "error"
	HealthUnknown = "unknown"
)

const (
	// healthTimeout bounds the whole health request.
	healthTimeout = 10 * time.Second
	// outboundCacheTTL is how long the outbound port check is reused, so
	// the page cannot make the mail service connect out on every request;
	// a failed attempt to ask the service is reused for outboundErrorTTL.
	outboundCacheTTL = 10 * time.Minute
	outboundErrorTTL = time.Minute
	// certWarnBefore: a certificate expiring sooner is a warning.
	certWarnBefore = 14 * 24 * time.Hour
)

// MailHealthCheck is one check of MailHealth.
type MailHealthCheck struct {
	ID       string   `json:"id"`
	State    string   `json:"state"`
	Found    []string `json:"found"`
	Expected []string `json:"expected"`
	Detail   string   `json:"detail"`
}

// MailHealth lists the checks in a fixed order.
type MailHealth struct {
	MailHost string            `json:"mail_host"`
	Checks   []MailHealthCheck `json:"checks"`
}

// mailHealthState caches the outbound check; the zero value is ready.
type mailHealthState struct {
	mu       sync.Mutex
	outbound *config.OutboundCheck
	err      error
	at       time.Time
	// running is held while the mail service is being asked.
	running sync.Mutex
}

func (h *mailHealthState) cached(now time.Time) (*config.OutboundCheck, error, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ttl := outboundCacheTTL
	if h.err != nil {
		ttl = outboundErrorTTL
	}
	if (h.outbound == nil && h.err == nil) || now.Sub(h.at) >= ttl || now.Before(h.at) {
		return nil, nil, false
	}
	return h.outbound, h.err, true
}

func (h *mailHealthState) put(r *config.OutboundCheck, err error, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.outbound, h.err, h.at = r, err, now
}

func check(id, state, detail string) MailHealthCheck {
	return MailHealthCheck{ID: id, State: state, Found: []string{}, Expected: []string{}, Detail: detail}
}

// MailHealth runs the deliverability checks (admins only, also with a
// read-only token).
func (s *Service) MailHealth(ctx context.Context, a Actor) (MailHealth, error) {
	if err := needAuth(a); err != nil {
		return MailHealth{}, err
	}
	if !a.Admin || a.Sites != nil {
		return MailHealth{}, errorf(Forbidden, "only admins can see the mail server's health")
	}
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	host := s.MailHost()
	ids := []string{HealthService, HealthMailHostDNS, HealthPTR, HealthOutboundPort, HealthCertificate, HealthListeners, HealthPostmaster, HealthBlocklists}
	out := MailHealth{MailHost: host, Checks: make([]MailHealthCheck, len(ids))}

	// The status of the mail service feeds three checks; ask once.
	var st *config.Status
	var stErr error
	if s.mail != nil {
		sctx, scancel := context.WithTimeout(ctx, mailStatusTimeout)
		v, err := s.mail.Status(sctx)
		scancel()
		if err == nil {
			st = &v
		}
		stErr = err
	}

	var wg sync.WaitGroup
	run := func(i int, fn func() MailHealthCheck) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := fn()
			c.ID = ids[i]
			if c.Found == nil {
				c.Found = []string{}
			}
			if c.Expected == nil {
				c.Expected = []string{}
			}
			out.Checks[i] = c
		}()
	}
	run(0, func() MailHealthCheck { return s.healthService(ctx, st, stErr) })
	run(1, func() MailHealthCheck { return s.healthMailHostDNS(ctx, host) })
	run(2, func() MailHealthCheck { return s.healthPTR(ctx, host) })
	run(3, func() MailHealthCheck { return s.healthOutbound(ctx) })
	run(4, func() MailHealthCheck { return s.healthCertificate(st) })
	run(5, func() MailHealthCheck { return s.healthListeners(st) })
	run(6, func() MailHealthCheck { return s.healthPostmaster(ctx) })
	run(7, func() MailHealthCheck { return s.healthBlocklists(s.store.Settings(ctx)) })
	wg.Wait()
	return out, nil
}

const noService = "The mail service is not set up on this server."

func (s *Service) healthService(ctx context.Context, st *config.Status, stErr error) MailHealthCheck {
	switch {
	case s.mail == nil:
		return check("", HealthError, noService)
	case st == nil:
		c := check("", HealthError, "The mail service could not be reached. Check that it is running on the server.")
		if errors.Is(stErr, context.DeadlineExceeded) {
			c.Detail = "The mail service did not answer in time."
		}
		return c
	case !s.mailCertsLoaded():
		return check("", HealthWarning, "The mail service runs, but the mail server's certificate is still being loaded; the settings are sent once it is.")
	}
	cfg, err := s.buildMailConfig(ctx)
	if err != nil {
		return check("", HealthUnknown, "The mail service runs, but the settings to send it could not be put together.")
	}
	if cfg.Hash() != st.ConfigHash {
		return check("", HealthWarning, "The mail service runs, but not with the current settings yet; they are sent again shortly.")
	}
	return check("", HealthOK, "The mail service runs with the current settings.")
}

func (s *Service) healthMailHostDNS(ctx context.Context, host string) MailHealthCheck {
	if host == "" {
		return check("", HealthError, "The name of the mail server is not set yet; set it in the settings.")
	}
	ips := s.effectiveServerIPs()
	c := check("", HealthUnknown, "")
	c.Expected = ipStrings(ips)
	if len(ips) == 0 {
		c.Detail = "The server's addresses are not set, so it cannot be checked where " + host + " points."
		return c
	}
	lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	r := dns.CheckDomain(lctx, s.resolver, host, ips)
	c.Found = r.Found
	switch r.State {
	case dns.PointsHere:
		c.State, c.Detail = HealthOK, host+" points to this server."
	case dns.PointsElsewhere:
		c.State, c.Detail = HealthError, fmt.Sprintf("%s points to %s, not (only) to this server. Change its A/AAAA records to the server's addresses.", host, strings.Join(r.Found, ", "))
	case dns.NoRecord:
		c.State, c.Detail = HealthError, host+" has no A or AAAA record yet. Create them with the server's addresses."
	default:
		c.Detail = "The address of " + host + " could not be looked up; try again in a moment."
	}
	return c
}

func (s *Service) addrResolver() dns.AddrResolver {
	if r, ok := s.resolver.(dns.AddrResolver); ok {
		return r
	}
	return nil
}

func (s *Service) healthPTR(ctx context.Context, host string) MailHealthCheck {
	ips := s.effectiveServerIPs()
	c := check("", HealthUnknown, "")
	switch {
	case host == "":
		c.Detail = "The name of the mail server is not set yet."
		return c
	case len(ips) == 0:
		c.Detail = "The server's addresses are not set."
		return c
	}
	r := s.addrResolver()
	if r == nil {
		c.Detail = "Reverse lookups are not possible here."
		return c
	}
	type result struct {
		names []string
		err   error
	}
	res := make([]result, len(ips))
	lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for i, ip := range ips {
		wg.Add(1)
		go func() {
			defer wg.Done()
			names, err := r.LookupAddr(lctx, ip.Unmap().String())
			res[i] = result{names, err}
		}()
	}
	wg.Wait()
	var bad4, bad6, unknown []string
	for i, ip := range ips {
		ip = ip.Unmap()
		c.Expected = append(c.Expected, ip.String()+" → "+host)
		var names []string
		for _, n := range res[i].names {
			names = append(names, strings.ToLower(strings.TrimSuffix(n, ".")))
		}
		err := res[i].err
		var de *net.DNSError
		if err != nil && !(errors.As(err, &de) && de.IsNotFound) {
			unknown = append(unknown, ip.String())
			c.Found = append(c.Found, ip.String()+" → (lookup failed)")
			continue
		}
		if len(names) == 0 {
			c.Found = append(c.Found, ip.String()+" → (none)")
		} else {
			c.Found = append(c.Found, ip.String()+" → "+strings.Join(names, ", "))
		}
		if slices.Contains(names, host) {
			continue
		}
		if ip.Is4() {
			bad4 = append(bad4, ip.String())
		} else {
			bad6 = append(bad6, ip.String())
		}
	}
	switch {
	case len(bad4) > 0:
		c.State = HealthError
		c.Detail = fmt.Sprintf("The reverse name (PTR) of %s is not %s; many receivers refuse mail from such an address. Set it at your hosting provider.", strings.Join(bad4, ", "), host)
	case len(bad6) > 0:
		c.State = HealthWarning
		c.Detail = fmt.Sprintf("The reverse name (PTR) of %s is not %s, so mail is sent over IPv4 only. Set it at your hosting provider to send over IPv6 too.", strings.Join(bad6, ", "), host)
	case len(unknown) > 0:
		c.Detail = fmt.Sprintf("The reverse name of %s could not be looked up; try again in a moment.", strings.Join(unknown, ", "))
	default:
		c.State, c.Detail = HealthOK, "Every server address has "+host+" as its reverse name."
	}
	if len(bad4) > 0 && len(bad6) > 0 {
		c.Detail += fmt.Sprintf(" The same goes for %s (IPv6).", strings.Join(bad6, ", "))
	}
	return c
}

// outbound returns the outbound port check, from the cache if it is fresh.
// A new check runs in the background with its own timeout, so a slow mail
// service fills the cache even after the request gave up; only one runs at
// a time.
func (s *Service) outbound(ctx context.Context) (*config.OutboundCheck, error, bool) {
	if r, err, ok := s.mailHealth.cached(s.now()); ok {
		return r, err, true
	}
	if !s.mailHealth.running.TryLock() {
		return nil, nil, false
	}
	if r, err, ok := s.mailHealth.cached(s.now()); ok { // filled meanwhile
		s.mailHealth.running.Unlock()
		return r, err, true
	}
	done := make(chan struct{})
	go func() {
		defer s.mailHealth.running.Unlock()
		defer close(done)
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mailTimeout)
		defer cancel()
		r, err := s.mail.CheckOutbound(cctx)
		if err != nil {
			s.mailHealth.put(nil, err, s.now())
			return
		}
		s.mailHealth.put(&r, nil, s.now())
	}()
	select {
	case <-done:
		return s.mailHealth.cached(s.now())
	case <-ctx.Done():
		return nil, nil, false
	}
}

func (s *Service) healthOutbound(ctx context.Context) MailHealthCheck {
	if s.mail == nil {
		return check("", HealthUnknown, noService)
	}
	r, err, ok := s.outbound(ctx)
	switch {
	case !ok:
		return check("", HealthUnknown, "Outgoing port 25 is being checked; look again in a moment.")
	case err != nil:
		return check("", HealthUnknown, "The mail service could not be asked to try outgoing port 25.")
	}
	c := check("", HealthOK, "")
	if r.Target != "" {
		c.Found = []string{r.Target}
	}
	if r.OK {
		c.Detail = "Connections to port 25 of other mail servers work."
		return c
	}
	c.State = HealthError
	c.Detail = "Connections to port 25 of other mail servers do not get through, so mail cannot be delivered. Many hosting providers block the port for new accounts; ask yours to unblock it."
	if e := strings.TrimSpace(r.Error); e != "" {
		c.Detail += " (" + e + ")"
	}
	return c
}

func (s *Service) healthCertificate(st *config.Status) MailHealthCheck {
	switch {
	case s.mail == nil:
		return check("", HealthUnknown, noService)
	case st == nil:
		return check("", HealthUnknown, "The mail service could not be asked about its certificate.")
	case st.CertificateExpires.IsZero():
		return check("", HealthError, "The mail service has no certificate, so mail programs cannot connect securely. It is requested automatically once the mail server's name points here.")
	}
	exp := st.CertificateExpires
	c := check("", HealthOK, "")
	c.Found = []string{exp.UTC().Format(time.RFC3339)}
	left := exp.Sub(s.now())
	switch {
	case left <= 0:
		c.State, c.Detail = HealthError, "The mail server's certificate has expired."
	case left < certWarnBefore:
		c.State, c.Detail = HealthWarning, fmt.Sprintf("The mail server's certificate expires in %d days and has not been renewed yet.", int(left.Hours()/24))
	default:
		c.Detail = "The mail server's certificate is valid until " + exp.UTC().Format("2006-01-02") + "."
	}
	return c
}

func (s *Service) healthListeners(st *config.Status) MailHealthCheck {
	switch {
	case s.mail == nil:
		return check("", HealthUnknown, noService)
	case st == nil:
		return check("", HealthUnknown, "The mail service could not be asked about its ports.")
	case len(st.Listeners) == 0:
		return check("", HealthWarning, "The mail service does not listen on any port yet.")
	}
	c := check("", HealthOK, "Every port of the mail service is listening.")
	var bad []string
	for _, l := range st.Listeners {
		label := l.Name + " " + l.Address
		switch {
		case l.Error != "":
			c.Found = append(c.Found, label+": "+l.Error)
			bad = append(bad, fmt.Sprintf("%s (%s): %s", l.Name, l.Address, l.Error))
		case !l.Listening:
			c.Found = append(c.Found, label+": not listening")
			bad = append(bad, fmt.Sprintf("%s (%s) is not listening", l.Name, l.Address))
		default:
			c.Found = append(c.Found, label+": listening")
		}
	}
	if len(bad) > 0 {
		c.State = HealthError
		c.Detail = "Some ports of the mail service do not work: " + strings.Join(bad, "; ") + ". Another program may be using the port."
	}
	return c
}

func (s *Service) healthPostmaster(ctx context.Context) MailHealthCheck {
	if s.state().postmasterAddress != "" {
		return check("", HealthOK, "Mail to postmaster@ and abuse@ that has no mailbox to go to is sent to "+s.state().postmasterAddress+".")
	}
	domains, err := s.store.MailDomains(ctx)
	if err != nil {
		return check("", HealthUnknown, "The mail domains could not be read.")
	}
	aliases, err := s.store.MailAliases(ctx, "")
	if err != nil {
		return check("", HealthUnknown, "The mail domains could not be read.")
	}
	var uncovered []string
	for _, d := range domains {
		if !d.Active || d.Mailboxes > 0 {
			continue
		}
		if slices.ContainsFunc(aliases, func(a store.MailAlias) bool { return a.Domain == d.Domain && a.LocalPart == "postmaster" }) {
			continue
		}
		uncovered = append(uncovered, d.Domain)
	}
	host := s.MailHost()
	if len(uncovered) > 0 {
		c := check("", HealthWarning, fmt.Sprintf("Mail to postmaster@ and abuse@ of %s is refused: these domains have no mailbox and no postmaster address is set. Set one in the settings.", strings.Join(uncovered, ", ")))
		c.Found = uncovered
		return c
	}
	detail := "Every active domain has a mailbox for postmaster@ and abuse@."
	if host != "" {
		detail += " Set a postmaster address in the settings to also receive mail to postmaster@" + host + "."
	}
	return check("", HealthOK, detail)
}
