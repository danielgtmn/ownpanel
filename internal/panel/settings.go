package panel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/dns"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// Panel settings: the panel's own domain, the server's public addresses and
// the e-mail for the certificate authority. They are set in the first-run
// wizard and stored in the database. The matching startup flags
// (-panel-domain, -server-ip, -acme-email) still work and win: a value set
// by flag is shown as locked and cannot be changed in the panel.
//
// Until the wizard is completed with a panel domain, the panel is also
// reachable at https://<server address>/ (certificate from Caddy's internal
// CA); afterwards those addresses redirect to the domain.

// Keys of the settings table.
const (
	keyPanelDomain   = "panel_domain"
	keyServerIPs     = "server_ips"
	keyACMEEmail     = "acme_email"
	keySetupComplete = "setup_complete"
	keyMailHost      = "mail_host"
	keyPostmaster    = "postmaster_address"
)

// certCheckTimeout bounds the certificate check of the wizard.
const certCheckTimeout = 5 * time.Second

// loopbackIPs always reach the panel through the web server, so it can be
// opened locally or through an SSH tunnel.
var loopbackIPs = []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("::1")}

// settingsState is what is stored; the flags are kept in Service.opts and
// Service.serverIPs.
type settingsState struct {
	panelDomain   string
	serverIPs     []netip.Addr
	acmeEmail     string
	setupComplete bool
	mailHost      string
	// postmasterAddress receives postmaster@ and abuse@ of the mail host
	// and of domains without mailboxes.
	postmasterAddress string
	// Spam checks (mailfilter.go): blocklists nil means the default list;
	// junkScore 0 means the default.
	blocklists  []blocklistEntry
	junkScore   float64
	greylisting bool
}

// DetectAddresses returns the global unicast addresses of the host's
// interfaces that are up. Nothing outside the machine is asked.
func DetectAddresses() ([]netip.Addr, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(n.IP); ok {
					out = append(out, ip)
				}
			}
		}
	}
	return out, nil
}

// DetectedIP is an address of this machine offered as a suggestion.
type DetectedIP struct {
	IP     string `json:"ip"`
	Public bool   `json:"public"`
}

// Settings is what admins see of the panel settings.
type Settings struct {
	PanelDomain       string       `json:"panel_domain"`
	PanelDomainLocked bool         `json:"panel_domain_locked"`
	ServerIPs         []string     `json:"server_ips"`
	ServerIPsLocked   bool         `json:"server_ips_locked"`
	DetectedIPs       []DetectedIP `json:"detected_ips"`
	ACMEEmail         string       `json:"acme_email"`
	ACMEEmailLocked   bool         `json:"acme_email_locked"`
	TLSMode           string       `json:"tls_mode"`
	SetupComplete     bool         `json:"setup_complete"`
	IPAccess          bool         `json:"ip_access"`
	MailHost          string       `json:"mail_host"`
	MailHostLocked    bool         `json:"mail_host_locked"`
	PostmasterAddress string       `json:"postmaster_address"`
	// MailBlocklists are the blocklist zones in effect (the default list
	// when MailBlocklistsDefault), access keys masked; empty means off.
	MailBlocklists        []string `json:"mail_blocklists"`
	MailBlocklistsDefault bool     `json:"mail_blocklists_default"`
	MailJunkScore         float64  `json:"mail_junk_score"`
	MailGreylisting       bool     `json:"mail_greylisting"`
}

// SettingsUpdate changes some settings; nil fields stay as they are, an
// empty value clears the setting.
type SettingsUpdate struct {
	PanelDomain       *string
	ServerIPs         *[]string
	ACMEEmail         *string
	MailHost          *string
	PostmasterAddress *string
	// MailBlocklistsDefault true returns to the default list; false alone
	// keeps the zones in effect as an own list. MailBlocklists sets an own
	// list ([] turns blocklists off); a masked zone keeps the stored one.
	MailBlocklistsDefault *bool
	MailBlocklists        *[]string
	MailJunkScore         *float64
	MailGreylisting       *bool
}

// DNSCheck tells whether the panel domain points at this server.
type DNSCheck struct {
	State    string   `json:"state"`
	Found    []string `json:"found"`
	Expected []string `json:"expected"`
}

// CertificateCheck tells whether a publicly trusted certificate is served
// for the panel domain.
type CertificateCheck struct {
	State  string `json:"state"`
	Detail string `json:"detail"`
}

// SettingsChecks are the readiness checks of the wizard.
type SettingsChecks struct {
	DNS         DNSCheck         `json:"dns"`
	Certificate CertificateCheck `json:"certificate"`
}

// Check states besides the domain states of DomainStatus.
const (
	CheckNotConfigured = "not_configured"
	CertReady          = "ready"
	CertPending        = "pending"
	CertNotApplicable  = "not_applicable"
)

// needAdminRead: admins, also with a read-only token, but not with a token
// limited to sites.
func needAdminRead(a Actor) error {
	if err := needAuth(a); err != nil {
		return err
	}
	if !a.Admin || a.Sites != nil {
		return errorf(Forbidden, "only admins can see the panel settings")
	}
	return nil
}

func (s *Service) loadSettings(ctx context.Context) error {
	m, err := s.store.Settings(ctx)
	if err != nil {
		return err
	}
	st := settingsState{
		panelDomain:       m[keyPanelDomain],
		acmeEmail:         m[keyACMEEmail],
		setupComplete:     m[keySetupComplete] == "true",
		mailHost:          m[keyMailHost],
		postmasterAddress: m[keyPostmaster],
	}
	loadFilterSettings(&st, m)
	if v := m[keyServerIPs]; v != "" {
		// Stored addresses were checked when saved; the TLS mode may have
		// changed since, which must not stop the daemon.
		ips, err := ParseServerIPs([]string{v}, true)
		if err != nil {
			slog.Warn("ignoring stored server addresses", "value", v, "err", err)
		}
		st.serverIPs = ips
	}
	s.settingsMu.Lock()
	s.settings = st
	s.settingsMu.Unlock()
	return nil
}

func (s *Service) state() settingsState {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	st := s.settings
	st.serverIPs = slices.Clone(st.serverIPs)
	st.blocklists = slices.Clone(st.blocklists)
	return st
}

// PanelDomain is the domain the panel is served on: -panel-domain, else the
// one from the settings; "" if none.
func (s *Service) PanelDomain() string {
	if s.opts.PanelDomain != "" {
		return s.opts.PanelDomain
	}
	return s.state().panelDomain
}

// effectiveServerIPs are -server-ip, else the addresses from the settings.
func (s *Service) effectiveServerIPs() []netip.Addr {
	if len(s.serverIPs) > 0 {
		return slices.Clone(s.serverIPs)
	}
	return s.state().serverIPs
}

// MailHost is the mail server's name: -mail-host, else the one from the
// settings; "" if none.
func (s *Service) MailHost() string {
	if s.mailHostFlag != "" {
		return s.mailHostFlag
	}
	return s.state().mailHost
}

func (s *Service) acmeEmail() string {
	if s.opts.ACMEEmail != "" {
		return s.opts.ACMEEmail
	}
	return s.state().acmeEmail
}

// ipAccess: the panel is served on the server's addresses until setup is
// complete with a panel domain.
func (s *Service) ipAccess() bool {
	return !(s.state().setupComplete && s.PanelDomain() != "")
}

// detected returns this machine's global unicast addresses, sorted.
func (s *Service) detected() []netip.Addr {
	if s.interfaces == nil {
		return nil
	}
	addrs, err := s.interfaces()
	if err != nil {
		slog.Warn("could not list the network addresses", "err", err)
		return nil
	}
	var out []netip.Addr
	for _, a := range addrs {
		a = a.Unmap().WithZone("")
		if a.IsGlobalUnicast() && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b netip.Addr) int { return a.Compare(b) })
	return out
}

// accessIPs are the addresses on which the web server answers for the panel
// itself: the configured and detected public addresses, and loopback.
func (s *Service) accessIPs() []string {
	ips := append(s.effectiveServerIPs(), loopbackIPs...)
	for _, a := range s.detected() {
		if isPublicUnicast(a) {
			ips = append(ips, a)
		}
	}
	var out []string
	for _, ip := range ips {
		if v := ip.String(); !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// caddyOptions are the web server options with the settings applied.
func (s *Service) caddyOptions() caddy.Options {
	o := s.opts
	o.PanelDomain = s.PanelDomain()
	o.ACMEEmail = s.acmeEmail()
	if o.PanelUpstream != "" {
		o.PanelIPs = s.accessIPs()
		o.IPRedirect = !s.ipAccess()
		// The mail host's certificate comes from the public CA only with
		// -tls acme; its challenges reach the panel through port 80.
		if o.TLSMode == caddy.TLSACME && s.certs != nil {
			o.MailHost = s.MailHost()
		}
	}
	return o
}

func ipStrings(ips []netip.Addr) []string {
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return out
}

func (s *Service) settingsView() Settings {
	st := s.state()
	v := Settings{
		PanelDomain:       s.PanelDomain(),
		PanelDomainLocked: s.opts.PanelDomain != "",
		ServerIPs:         ipStrings(s.effectiveServerIPs()),
		ServerIPsLocked:   len(s.serverIPs) > 0,
		DetectedIPs:       []DetectedIP{},
		ACMEEmail:         s.acmeEmail(),
		ACMEEmailLocked:   s.opts.ACMEEmail != "",
		TLSMode:           s.opts.TLSMode,
		SetupComplete:     st.setupComplete,
		IPAccess:          s.ipAccess(),
		MailHost:          s.MailHost(),
		MailHostLocked:    s.mailHostFlag != "",
		PostmasterAddress: st.postmasterAddress,
		MailJunkScore:     effectiveJunkScore(st.junkScore),
		MailGreylisting:   st.greylisting,
	}
	v.MailBlocklists, v.MailBlocklistsDefault = blocklistView(st.blocklists)
	for _, a := range s.detected() {
		v.DetectedIPs = append(v.DetectedIPs, DetectedIP{IP: a.String(), Public: isPublicUnicast(a)})
	}
	return v
}

// SetupComplete reports whether the first-run wizard is done: there is a
// user and the wizard was completed (or the install predates it).
func (s *Service) SetupComplete(ctx context.Context) (bool, error) {
	needed, err := s.SetupRequired(ctx)
	if err != nil || needed {
		return false, err
	}
	return s.state().setupComplete, nil
}

// Settings returns the panel settings.
func (s *Service) Settings(ctx context.Context, a Actor) (Settings, error) {
	if err := needAdminRead(a); err != nil {
		return Settings{}, err
	}
	return s.settingsView(), nil
}

func lockedError(flag string) error {
	return errorf(Conflict, "This value is set in the server configuration (%s) and cannot be changed here.", flag)
}

// validPanelDomain normalizes a panel domain: a plain host name with a dot,
// not an address, not used by any site.
func (s *Service) validPanelDomain(ctx context.Context, domain string) (string, error) {
	if _, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(domain), "[]")); err == nil {
		return "", errorf(Invalid, "the panel needs a domain name like panel.example.com, not an IP address")
	}
	d, err := store.NormalizeDomain(domain)
	if err != nil {
		return "", errorf(Invalid, "%q is not a valid domain name; enter one like panel.example.com", strings.TrimSpace(domain))
	}
	labels := strings.Split(d, ".")
	if _, err := strconv.Atoi(labels[len(labels)-1]); err == nil {
		return "", errorf(Invalid, "the panel needs a domain name like panel.example.com, not an IP address")
	}
	sites, err := s.store.Sites(ctx)
	if err != nil {
		return "", storeError(err)
	}
	for _, site := range sites {
		if slices.Contains(site.Domains, d) {
			return "", errorf(Conflict, "the domain %s is already used by the site %q", d, site.Name)
		}
	}
	if used, err := s.visibleDomainRedirect(ctx, System, d); err != nil || used {
		if err != nil {
			return "", err
		}
		return "", errorf(Conflict, "the domain %s redirects elsewhere; remove that redirect first", d)
	}
	return d, nil
}

// validMailHost normalizes a mail host: a plain host name with a dot, not
// an address, not a site or redirect domain and not a mail domain. It may
// be the panel domain.
func (s *Service) validMailHost(ctx context.Context, domain string) (string, error) {
	if _, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(domain), "[]")); err == nil {
		return "", errorf(Invalid, "the mail server needs a name like mail.example.com, not an IP address")
	}
	d, err := store.NormalizeDomain(domain)
	labels := strings.Split(d, ".")
	if err != nil || strings.Trim(labels[len(labels)-1], "0123456789") == "" {
		return "", errorf(Invalid, "%q is not a valid name; enter one like mail.example.com", strings.TrimSpace(domain))
	}
	sites, err := s.store.Sites(ctx)
	if err != nil {
		return "", storeError(err)
	}
	for _, site := range sites {
		if slices.Contains(site.Domains, d) {
			return "", errorf(Conflict, "the domain %s is already used by the site %q", d, site.Name)
		}
	}
	if used, err := s.visibleDomainRedirect(ctx, System, d); err != nil || used {
		if err != nil {
			return "", err
		}
		return "", errorf(Conflict, "the domain %s redirects elsewhere; remove that redirect first", d)
	}
	if _, err := s.store.MailDomain(ctx, d); err == nil {
		return "", errorf(Conflict, "e-mail is enabled for %s as a mail domain; the mail server needs a name of its own, like mail.%s", d, d)
	}
	return d, nil
}

func validACMEEmail(v string) (string, error) {
	v = strings.TrimSpace(v)
	a, err := mail.ParseAddress(v)
	if err != nil || a.Address != v || a.Name != "" || len(v) > 254 || !strings.Contains(v[strings.LastIndex(v, "@")+1:], ".") {
		return "", errorf(Invalid, "%q is not a valid e-mail address", v)
	}
	return v, nil
}

// UpdateSettings changes the panel settings and applies them to the web
// server at once. If only that update fails, the settings are saved and
// returned together with a NotSynced error.
func (s *Service) UpdateSettings(ctx context.Context, a Actor, u SettingsUpdate) (Settings, error) {
	if err := needAdmin(a); err != nil {
		return Settings{}, err
	}
	s.settingsWrite.Lock()
	defer s.settingsWrite.Unlock()
	cur := s.state()
	next := cur
	values := map[string]string{}
	var changed []string

	if u.PanelDomain != nil {
		if s.opts.PanelDomain != "" {
			return Settings{}, lockedError("-panel-domain")
		}
		d := strings.TrimSpace(*u.PanelDomain)
		if d != "" {
			var err error
			if d, err = s.validPanelDomain(ctx, d); err != nil {
				return Settings{}, err
			}
		}
		if d != cur.panelDomain {
			next.panelDomain, values[keyPanelDomain] = d, d
			changed = append(changed, "panel domain "+orRemoved(d))
		}
	}
	if u.ServerIPs != nil {
		if len(s.serverIPs) > 0 {
			return Settings{}, lockedError("-server-ip")
		}
		allowNonPublic := s.opts.TLSMode == caddy.TLSInternal || s.opts.TLSMode == caddy.TLSOff
		ips, err := ParseServerIPs(*u.ServerIPs, allowNonPublic)
		if err != nil {
			return Settings{}, &Error{Kind: Invalid, Msg: err.Error(), Err: err}
		}
		if !slices.Equal(ips, cur.serverIPs) {
			joined := strings.Join(ipStrings(ips), ",")
			next.serverIPs, values[keyServerIPs] = ips, joined
			changed = append(changed, "server addresses "+orRemoved(strings.ReplaceAll(joined, ",", ", ")))
		}
	}
	if u.ACMEEmail != nil {
		if s.opts.ACMEEmail != "" {
			return Settings{}, lockedError("-acme-email")
		}
		e := strings.TrimSpace(*u.ACMEEmail)
		if e != "" {
			var err error
			if e, err = validACMEEmail(e); err != nil {
				return Settings{}, err
			}
		}
		if e != cur.acmeEmail {
			next.acmeEmail, values[keyACMEEmail] = e, e
			// The address itself stays out of the log.
			if e == "" {
				changed = append(changed, "certificate e-mail removed")
			} else {
				changed = append(changed, "certificate e-mail changed")
			}
		}
	}
	if u.MailHost != nil {
		if s.mailHostFlag != "" {
			return Settings{}, lockedError("-mail-host")
		}
		d := strings.TrimSpace(*u.MailHost)
		if d != "" {
			var err error
			if d, err = s.validMailHost(ctx, d); err != nil {
				return Settings{}, err
			}
		}
		if d != cur.mailHost {
			next.mailHost, values[keyMailHost] = d, d
			changed = append(changed, "mail server name "+orRemoved(d))
		}
	}
	if u.PostmasterAddress != nil {
		e := strings.ToLower(strings.TrimSpace(*u.PostmasterAddress))
		if e != "" && !config.ValidAddress(e) {
			return Settings{}, errorf(Invalid, "%q is not a valid e-mail address", strings.TrimSpace(*u.PostmasterAddress))
		}
		if e != cur.postmasterAddress {
			next.postmasterAddress, values[keyPostmaster] = e, e
			changed = append(changed, "postmaster address "+orRemoved(e))
		}
	}
	if err := s.updateFilterSettings(u, cur, &next, values, &changed); err != nil {
		return Settings{}, err
	}
	if len(values) == 0 {
		return s.settingsView(), nil
	}
	if err := s.store.PutSettings(ctx, values); err != nil {
		return Settings{}, storeError(err)
	}
	s.settingsMu.Lock()
	s.settings = next
	s.settingsMu.Unlock()
	if _, ok := values[keyServerIPs]; ok {
		s.dnsCache.dropAllChecks()
	}
	s.record(ctx, a, "settings.update", "", strings.Join(changed, "; "))
	err := s.syncSaved(ctx)
	_, mailHost := values[keyMailHost]
	_, postmaster := values[keyPostmaster]
	_, email := values[keyACMEEmail]
	_, blocklists := values[keyMailBlocklists]
	_, junk := values[keyMailJunkScore]
	_, grey := values[keyMailGreylisting]
	if mailHost || postmaster || email || blocklists || junk || grey {
		if mailHost || email {
			// Certificates for the new name are looked for before the push;
			// new ones are requested by the background loop.
			s.refreshMailCerts(context.WithoutCancel(ctx), false)
		}
		if merr := s.mailSaved(ctx); err == nil {
			err = merr
		}
	}
	return s.settingsView(), err
}

func orRemoved(v string) string {
	if v == "" {
		return "removed"
	}
	return v
}

// CompleteSetup ends the first-run wizard. With a panel domain set, the
// server's addresses then redirect to it. Completing again changes nothing.
func (s *Service) CompleteSetup(ctx context.Context, a Actor) (Settings, error) {
	if err := needAdmin(a); err != nil {
		return Settings{}, err
	}
	s.settingsWrite.Lock()
	defer s.settingsWrite.Unlock()
	if s.state().setupComplete {
		return s.settingsView(), nil
	}
	if err := s.store.PutSettings(ctx, map[string]string{keySetupComplete: "true"}); err != nil {
		return Settings{}, storeError(err)
	}
	s.settingsMu.Lock()
	s.settings.setupComplete = true
	s.settingsMu.Unlock()
	detail := "panel domain " + s.PanelDomain()
	if s.PanelDomain() == "" {
		detail = "without a panel domain; the panel stays reachable by IP address"
	}
	s.record(ctx, a, "setup.complete", "", detail)
	err := s.syncSaved(ctx)
	return s.settingsView(), err
}

// SettingsChecks tells whether the panel domain points at this server and
// whether a trusted certificate is served for it. Both are looked up afresh.
func (s *Service) SettingsChecks(ctx context.Context, a Actor) (SettingsChecks, error) {
	if err := needAdminRead(a); err != nil {
		return SettingsChecks{}, err
	}
	domain := s.PanelDomain()
	expected := ipStrings(s.effectiveServerIPs())
	out := SettingsChecks{
		DNS:         DNSCheck{State: CheckNotConfigured, Found: []string{}, Expected: expected},
		Certificate: CertificateCheck{State: CheckNotConfigured, Detail: "No panel domain is set yet."},
	}
	if s.opts.TLSMode != caddy.TLSACME {
		out.Certificate = CertificateCheck{State: CertNotApplicable, Detail: "This server does not request public certificates (it was started with -tls " + s.opts.TLSMode + ")."}
	}
	if domain == "" {
		return out, nil
	}
	var wg sync.WaitGroup
	if s.opts.TLSMode == caddy.TLSACME {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out.Certificate = s.checkCertificate(ctx, domain)
		}()
	}
	lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	c := dns.CheckDomain(lctx, s.resolver, domain, s.effectiveServerIPs())
	out.DNS.State = stateName(c.State)
	if c.Found != nil {
		out.DNS.Found = c.Found
	}
	wg.Wait()
	if ctx.Err() != nil {
		return SettingsChecks{}, newError(Internal, ctx.Err())
	}
	return out, nil
}

// checkCertificate connects to the local web server with the panel domain
// as SNI and verifies the certificate like a browser would.
func (s *Service) checkCertificate(ctx context.Context, domain string) CertificateCheck {
	addr := s.certAddr
	if addr == "" {
		addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(s.opts.HTTPSPort))
	}
	ctx, cancel := context.WithTimeout(ctx, certCheckTimeout)
	defer cancel()
	d := tls.Dialer{Config: &tls.Config{ServerName: domain, RootCAs: s.certRoots, MinVersion: tls.VersionTLS12}}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err == nil {
		conn.Close()
		return CertificateCheck{State: CertReady, Detail: fmt.Sprintf("A trusted certificate for %s is in place.", domain)}
	}
	var (
		unknown  x509.UnknownAuthorityError
		hostname x509.HostnameError
		invalid  x509.CertificateInvalidError
		tlsErr   *tls.CertificateVerificationError
	)
	switch {
	case errors.As(err, &unknown), errors.As(err, &hostname), errors.As(err, &invalid), errors.As(err, &tlsErr):
		return CertificateCheck{State: CertPending, Detail: fmt.Sprintf("There is no trusted certificate for %s yet. It is requested automatically once the domain points at this server; this usually takes a minute.", domain)}
	case errors.Is(err, context.DeadlineExceeded):
		return CertificateCheck{State: CertPending, Detail: "The web server did not answer in time; try again in a moment."}
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) && netErr.Op == "dial" {
		return CertificateCheck{State: CertPending, Detail: "The web server could not be reached to check the certificate."}
	}
	// A failed handshake: the web server has no certificate for the name yet.
	return CertificateCheck{State: CertPending, Detail: fmt.Sprintf("There is no certificate for %s yet. It is requested automatically once the domain points at this server; this usually takes a minute.", domain)}
}
