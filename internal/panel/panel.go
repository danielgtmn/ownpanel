// Package panel is the single place where the panel's operations are
// defined, including who may do what. HTTP, MCP and other front doors only
// authenticate and translate to and from it, so every one of them does
// exactly the same thing and enforces the same permissions.
package panel

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/danielgtmn/sesam/v2"
	"github.com/danielgtmn/sesam/v2/plugins/password"

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/deploy"
	"github.com/danielgtmn/ownpanel/internal/dns"
	"github.com/danielgtmn/ownpanel/internal/github"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/store"
	"github.com/danielgtmn/panzerschrank"
)

// WebServer is what the service needs from the web server.
type WebServer interface {
	Load(ctx context.Context, cfg any) error
}

// Config is what New needs.
type Config struct {
	Store   *store.Store
	Deploys *deploy.Manager
	Web     WebServer
	// Caddy are the web server options. PanelDomain and ACMEEmail, when set,
	// come from the startup flags: they override (and lock) the settings.
	// PanelUpstream should always be set: without it the panel is not
	// served through the web server at all.
	Caddy caddy.Options
	// SetupCodeFile holds the one-time code for creating the first admin.
	SetupCodeFile string

	// Secrets encrypts the tokens of DNS connections. Without it no DNS
	// connection can be added or used.
	Secrets *panzerschrank.Box
	// ServerIPs are the server's public addresses from -server-ip, which
	// domains should point at (see ParseServerIPs). Set, they override (and
	// lock) the addresses from the settings.
	ServerIPs []netip.Addr
	// Interfaces lists the machine's addresses, offered as suggestions and
	// served for the panel before setup; nil means DetectAddresses.
	Interfaces func() ([]netip.Addr, error)
	// CertRoots verify the panel domain's certificate in the readiness
	// check; nil means the system roots. Tests pass their own CA.
	CertRoots *x509.CertPool
	// CertCheckAddr is where that check connects; "" means
	// 127.0.0.1:<HTTPSPort>.
	CertCheckAddr string
	// NewDNSProvider returns a client for a DNS provider account; nil means
	// DefaultDNSProvider. Tests pass a fake.
	NewDNSProvider func(provider, token string) (dns.Provider, error)
	// Resolver looks up where domains point; nil means asking the domain's
	// authoritative nameservers directly (which sees a new record at once,
	// unlike a recursive resolver that caches "no such domain" for an hour),
	// falling back to the system resolver.
	Resolver dns.Resolver
	// Updates says where new releases come from and where the root-side
	// updater picks up requests. Without Dir, updates are not offered.
	Updates UpdatesConfig

	// Mail is the mail service's admin socket (-mail-admin); nil means no
	// mail service is set up: mail domains, mailboxes and aliases are still
	// stored, but nothing is pushed. *config.Client satisfies it.
	Mail MailService
	// MailHost, when set, comes from -mail-host: it overrides (and locks)
	// the setting.
	MailHost string
	// MailTLSDir keeps the ACME account and the certificates of the mail
	// host; "" means none are obtained (tests).
	MailTLSDir string
	// Builds is the build service's socket (-build-worker); nil means
	// builds are not set up for this panel. *build.Client satisfies it.
	Builds BuildService
	// BuildsStateFile is install.sh's record of the build environment
	// (build.StateFile); "" means there is none.
	BuildsStateFile string
	// MXResolver looks up MX records to activate mail domains; nil means
	// Resolver if it can, else (only when Resolver is nil too) asking the
	// authoritative nameservers.
	MXResolver dns.MXResolver

	// GitHub is where GitHub connections are made (github.go); nil means
	// github.com. Tests pass a client for a fake.
	GitHub *github.Client
}

// Service owns every use case of the panel.
type Service struct {
	store         *store.Store
	deploys       *deploy.Manager
	web           WebServer
	opts          caddy.Options
	setupCodeFile string

	now func() time.Time
	// auth signs people in (users, passwords, sessions, reset links; see
	// signin.go), pw is its password plugin. passwordChecks bounds the
	// password checks in flight.
	auth           *sesam.Auth
	pw             *password.Plugin
	passwordChecks chan struct{}
	uploads        uploads

	// syncMu serializes Sync so concurrent changes cannot load configs out
	// of order.
	syncMu sync.Mutex
	// unhealthyRetry is how soon Run retries while the web server is out of
	// sync. A field so tests can lower it.
	unhealthyRetry time.Duration
	// startupGrace is how long Run waits quietly at startup for the web
	// server, retrying every startupRetry. Fields so tests can lower them.
	startupGrace time.Duration
	startupRetry time.Duration

	secrets     *panzerschrank.Box
	serverIPs   []netip.Addr
	newProvider func(provider, token string) (dns.Provider, error)
	resolver    dns.Resolver
	dnsCache    *dnsCache

	// settingsMu guards settings; settingsWrite serializes changes to them.
	settingsMu    sync.RWMutex
	settings      settingsState
	settingsWrite sync.Mutex
	interfaces    func() ([]netip.Addr, error)
	certRoots     *x509.CertPool
	certAddr      string

	updates *updater

	mail         MailService
	mailHostFlag string
	mx           dns.MXResolver
	certs        *mailCerts
	// mailSyncMu serializes mailSync, independent of syncMu.
	mailSyncMu sync.Mutex
	// mailEventsMu serializes recordMailEvents (the background loop and
	// an admin's change that catches up first).
	mailEventsMu sync.Mutex
	// failAccountEvent, when set (tests), makes recording an account event
	// fail.
	failAccountEvent func(config.Event) error
	// mailLeftOut is what buildMailConfig last left out, so it is logged
	// once (mailsync.go).
	mailLeftOutMu sync.Mutex
	mailLeftOut   string
	// mailHealth caches the outbound port check of MailHealth.
	mailHealth mailHealthState
	// setupHosts knows which names for setting up mail programs point
	// here (mailsetup.go).
	setupHosts setupHostsState
	// importRate limits imports per person (mailtransfer.go).
	importRate importRate
	// previewSecret derives the addresses and keys of previews
	// (previews.go). Set once in New.
	previewSecret []byte
	// appKeyMu guards appKey, the app password key once read or made
	// (mailaccounts.go).
	appKeyMu sync.Mutex
	appKey   string

	builds          BuildService
	buildsStateFile string
	// github talks to GitHub; githubFlows are connections waiting for the
	// person to come back from GitHub; githubRepos caches repository
	// lists (github.go).
	github      *github.Client
	githubFlows githubFlows
	githubRepos githubRepoCache
	// githubHooks limits GitHub's deliveries (github_webhook.go).
	githubHooks githubHooks
	// gitChecks limits connections people make to Git hosts, gitHooks
	// the notifications of Git hosts (gitsource.go, gitnotify.go).
	gitChecks gitChecks
	gitHooks  gitHooks
}

// New returns a Service. It rejects invalid web server options up front, and
// a panel domain that a site already uses.
func New(cfg Config) (*Service, error) {
	if _, err := caddy.Build(cfg.Caddy, nil); err != nil {
		return nil, err
	}
	s := &Service{
		store:           cfg.Store,
		deploys:         cfg.Deploys,
		web:             cfg.Web,
		opts:            cfg.Caddy,
		setupCodeFile:   cfg.SetupCodeFile,
		now:             time.Now,
		passwordChecks:  make(chan struct{}, maxPasswordChecks),
		unhealthyRetry:  2 * time.Second,
		startupGrace:    15 * time.Second,
		startupRetry:    250 * time.Millisecond,
		secrets:         cfg.Secrets,
		serverIPs:       slices.Clone(cfg.ServerIPs),
		newProvider:     cfg.NewDNSProvider,
		resolver:        cfg.Resolver,
		dnsCache:        newDNSCache(),
		interfaces:      cfg.Interfaces,
		certRoots:       cfg.CertRoots,
		certAddr:        cfg.CertCheckAddr,
		updates:         newUpdater(cfg.Updates),
		mail:            cfg.Mail,
		builds:          cfg.Builds,
		buildsStateFile: cfg.BuildsStateFile,
		mx:              cfg.MXResolver,
		certs:           newMailCerts(cfg.MailTLSDir, cfg.Caddy.TLSMode),
		github:          cfg.GitHub,
	}
	if s.github == nil {
		s.github = DefaultGitHub()
	}
	if cfg.MailHost != "" {
		d, err := store.NormalizeDomain(cfg.MailHost)
		if err != nil {
			return nil, fmt.Errorf("mail host: %w", err)
		}
		s.mailHostFlag = d
	}
	if s.interfaces == nil {
		s.interfaces = DetectAddresses
	}
	if s.newProvider == nil {
		s.newProvider = DefaultDNSProvider
	}
	if s.resolver == nil {
		s.resolver = dns.NewAuthoritative(net.DefaultResolver)
	}
	if s.mx == nil {
		if mx, ok := s.resolver.(dns.MXResolver); ok {
			s.mx = mx
		} else {
			s.mx = noMX{}
		}
	}
	if d := cfg.Caddy.PanelDomain; d != "" && s.store != nil {
		sites, err := s.store.Sites(context.Background())
		if err != nil {
			return nil, err
		}
		for _, site := range sites {
			if slices.Contains(site.Domains, d) {
				return nil, fmt.Errorf("panel domain %s is already used by site %q", d, site.Name)
			}
		}
	}
	if d := s.mailHostFlag; d != "" && s.store != nil {
		sites, err := s.store.Sites(context.Background())
		if err != nil {
			return nil, err
		}
		for _, site := range sites {
			if slices.Contains(site.Domains, d) {
				return nil, fmt.Errorf("mail host %s is already used by site %q", d, site.Name)
			}
		}
	}
	if s.store != nil {
		if err := s.loadSettings(context.Background()); err != nil {
			return nil, err
		}
		if err := s.initPreviewSecret(context.Background()); err != nil {
			return nil, fmt.Errorf("preview secret: %w", err)
		}
		if err := s.initAuth(context.Background()); err != nil {
			return nil, fmt.Errorf("sign-in: %w", err)
		}
	}
	return s, nil
}

// Site is what callers see of a site.
type Site struct {
	Name     string   `json:"name"`
	Domains  []string `json:"domains"`
	Owner    string   `json:"owner"`
	Current  string   `json:"current_release"`
	Releases []string `json:"releases"`
	// Versions are the kept releases, newest first, with their state.
	Versions []Version `json:"versions"`
	// PreviewAddresses: versions can be opened on their own address (an
	// admin has set a domain for previews).
	PreviewAddresses bool `json:"preview_addresses"`
}

// Version is one kept release of a site.
type Version struct {
	ID   string `json:"id"`
	Live bool   `json:"live"`
	// Pending: published without being made live, and not live since.
	Pending bool `json:"pending"`
	// PreviewURL opens this version on its own address, whether it is live
	// or not; "" when previews have no addresses. Anyone with the link can
	// open it.
	PreviewURL string `json:"preview_url"`
}

func (s *Service) view(site store.Site) Site {
	return Site{
		Name:             site.Name,
		Domains:          site.Domains,
		Owner:            site.Owner,
		Current:          s.deploys.Current(site.Name),
		Releases:         s.deploys.Releases(site.Name),
		Versions:         s.versionsView(site.Name),
		PreviewAddresses: s.previewsServed(),
	}
}

// visibleSite returns a site the actor may see. Sites it may not see answer
// NotFound like missing ones, so names do not leak.
func (s *Service) visibleSite(ctx context.Context, a Actor, name string) (store.Site, error) {
	if err := needAuth(a); err != nil {
		return store.Site{}, err
	}
	site, err := s.store.Site(ctx, name)
	if err != nil {
		return store.Site{}, storeError(err)
	}
	if !a.canSee(site) {
		return store.Site{}, errorf(NotFound, "site not found")
	}
	return site, nil
}

func needWrite(a Actor) error {
	if a.ReadOnly {
		return errorf(Forbidden, "this access token can only read")
	}
	return nil
}

// Sites lists the sites the actor may see, sorted by name.
func (s *Service) Sites(ctx context.Context, a Actor) ([]Site, error) {
	if err := needAuth(a); err != nil {
		return nil, err
	}
	all, err := s.store.Sites(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	out := []Site{}
	for _, site := range all {
		if a.canSee(site) {
			out = append(out, s.view(site))
		}
	}
	return out, nil
}

// Site returns one site.
func (s *Service) Site(ctx context.Context, a Actor, name string) (Site, error) {
	site, err := s.visibleSite(ctx, a, name)
	if err != nil {
		return Site{}, err
	}
	return s.view(site), nil
}

// SaveSite creates a site (owned by the actor) or replaces its domains. If
// only the web server update fails, the site is saved: it is returned
// together with a NotSynced error.
func (s *Service) SaveSite(ctx context.Context, a Actor, name string, domains []string) (Site, error) {
	if err := needAuth(a); err != nil {
		return Site{}, err
	}
	clean, err := store.NormalizeDomains(domains)
	if err != nil {
		return Site{}, storeError(err)
	}
	if d := s.PanelDomain(); d != "" && slices.Contains(clean, d) {
		return Site{}, errorf(Invalid, "domain %q is the address of the panel itself", d)
	}
	if d := s.MailHost(); d != "" && slices.Contains(clean, d) {
		return Site{}, errorf(Invalid, "domain %q is the name of the mail server", d)
	}
	for _, d := range clean {
		if s.underPreviewDomain(d) {
			return Site{}, errUnderPreviewDomain(d, s.PreviewDomain())
		}
	}

	existing, err := s.store.Site(ctx, name)
	var site store.Site
	action := "site.domains"
	switch {
	case err == nil:
		if !a.canSee(existing) {
			// Creating would fail anyway; say so without revealing more.
			return Site{}, errorf(Conflict, "the site name %q is already taken", name)
		}
		if err := needWrite(a); err != nil {
			return Site{}, err
		}
		if err := s.othersMailDomainName(ctx, a, clean, existing.Domains); err != nil {
			return Site{}, err
		}
		site, err = s.store.SetDomains(ctx, name, clean)
	case errors.Is(err, store.ErrNotFound):
		if err := needWrite(a); err != nil {
			return Site{}, err
		}
		if a.Sites != nil {
			return Site{}, errorf(Forbidden, "this access token is limited to certain sites and cannot create new ones")
		}
		if a.Via == ViaSystem {
			return Site{}, errorf(Forbidden, "sites must be created by a user")
		}
		if err := s.othersMailDomainName(ctx, a, clean, nil); err != nil {
			return Site{}, err
		}
		if err := store.ValidateName(name); err != nil {
			return Site{}, storeError(err)
		}
		action = "site.create"
		site, err = s.createSite(ctx, name, clean, a.UserID)
	}
	if err != nil {
		return Site{}, s.domainError(ctx, a, err)
	}
	s.record(ctx, a, action, name, strings.Join(site.Domains, ", "))
	return s.view(site), s.syncSaved(ctx)
}

// createSite stores a new site. Files left behind by an earlier site of
// the same name (a removal that failed) are removed first, so they never
// become the new site's releases. syncMu keeps removeOrphanSites from
// deciding about the name meanwhile.
func (s *Service) createSite(ctx context.Context, name string, domains []string, owner string) (store.Site, error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	if err := s.deploys.Remove(name); err != nil {
		return store.Site{}, newError(Internal, err)
	}
	return s.store.CreateSite(ctx, name, domains, owner)
}

// domainError hides the name of a site the actor may not see when a domain
// is taken by it.
func (s *Service) domainError(ctx context.Context, a Actor, err error) error {
	var taken *store.DomainTakenError
	if errors.As(err, &taken) {
		if other, e := s.store.Site(ctx, taken.Site); e != nil || !a.canSee(other) {
			return &Error{Kind: Invalid, Msg: fmt.Sprintf("domain %q is already used by another site", taken.Domain), Err: err}
		}
	}
	return storeError(err)
}

// DeleteSite stops serving a site, then removes its files. If the web server
// cannot be updated the files stay for now, so nothing is served from a
// half-removed directory; the next successful sync removes them
// (removeOrphanSites).
func (s *Service) DeleteSite(ctx context.Context, a Actor, name string) error {
	if _, err := s.visibleSite(ctx, a, name); err != nil {
		return err
	}
	if err := needWrite(a); err != nil {
		return err
	}
	if a.Sites != nil {
		return errorf(Forbidden, "this access token is limited to certain sites and cannot delete sites")
	}
	if err := s.store.DeleteSite(ctx, name); err != nil {
		return storeError(err)
	}
	s.record(ctx, a, "site.delete", name, "")
	if err := s.syncSaved(ctx); err != nil {
		return err
	}
	if err := s.deploys.Remove(name); err != nil {
		return newError(Internal, err)
	}
	return nil
}

// Publish unpacks an archive as a new release and makes it live.
func (s *Service) Publish(ctx context.Context, a Actor, name string, archive io.Reader) (Site, error) {
	p, err := s.PublishVersion(ctx, a, name, archive, true)
	return p.Site, err
}

// Publication is the site after publishing, with the new version.
type Publication struct {
	Site
	// Release is the id of the new version.
	Release string `json:"published_release"`
	// Live: the new version is what visitors see now.
	Live bool `json:"published_live"`
	// PreviewURL opens the new version on its own address; "" when
	// previews have no addresses.
	PreviewURL string `json:"preview_url"`
}

// PublishVersion unpacks an archive as a new release. With live it is made
// live at once; without, the live release stays as it is (a site that has
// none keeps showing that it has no content yet) and the new one can be
// opened as a preview and made live later with MakeLive. If only the web
// server update fails, the publication is returned with a NotSynced error.
func (s *Service) PublishVersion(ctx context.Context, a Actor, name string, archive io.Reader, live bool) (Publication, error) {
	site, err := s.visibleSite(ctx, a, name)
	if err != nil {
		return Publication{}, err
	}
	if err := needWrite(a); err != nil {
		return Publication{}, err
	}
	first := s.deploys.Current(site.Name) == ""
	add, action := s.deploys.Deploy, "site.publish"
	if !live {
		add, action = s.deploys.Add, "site.publish-not-live"
	}
	id, err := add(ctx, site.Name, archive)
	if err != nil {
		kind := Internal
		if errors.Is(err, deploy.ErrBadArchive) {
			kind = Invalid
		}
		return Publication{}, newError(kind, err)
	}
	slog.Info("deployed", "site", site.Name, "release", id, "live", live)
	s.record(ctx, a, action, site.Name, id)
	p := Publication{Site: s.view(site), Release: id, Live: live, PreviewURL: s.previewURL(site.Name, id)}
	// Making a release live is a symlink switch; the web server config
	// changes only for the first one (placeholder -> files) and for
	// previews (one route per kept release).
	if (live && first) || s.previewsServed() {
		return p, s.syncSaved(ctx)
	}
	return p, nil
}

// MakeLive switches a site to an existing release; an empty release means the
// one before the current that was live itself.
func (s *Service) MakeLive(ctx context.Context, a Actor, name, release string) (Site, error) {
	site, err := s.visibleSite(ctx, a, name)
	if err != nil {
		return Site{}, err
	}
	if err := needWrite(a); err != nil {
		return Site{}, err
	}
	first := s.deploys.Current(site.Name) == ""
	id, err := s.deploys.Rollback(site.Name, release)
	if err != nil {
		kind := Internal
		if errors.Is(err, deploy.ErrNoRelease) {
			kind = Conflict
		}
		return Site{}, newError(kind, err)
	}
	slog.Info("made live", "site", site.Name, "release", id)
	s.record(ctx, a, "site.make-live", site.Name, id)
	if first {
		// The site had only versions that were never live: it served the
		// placeholder until now.
		return s.view(site), s.syncSaved(ctx)
	}
	return s.view(site), nil
}

// Sync is the reconciler: desired state in, full web server config out.
// Only the system and unrestricted admins may trigger it.
func (s *Service) Sync(ctx context.Context, a Actor) error {
	if a.Via != ViaSystem {
		if err := needAdmin(a); err != nil {
			return err
		}
	}
	return s.sync(ctx)
}

func (s *Service) sync(ctx context.Context) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	all, err := s.store.Sites(ctx)
	if err != nil {
		return err
	}
	var sites []caddy.Site
	for _, site := range all {
		sites = append(sites, caddy.Site{
			Name:     site.Name,
			Domains:  site.Domains,
			Root:     s.deploys.Root(site.Name),
			Live:     s.deploys.Current(site.Name) != "",
			Previews: s.caddyPreviews(site.Name),
		})
	}
	redirects, err := s.caddyRedirects(ctx)
	if err != nil {
		return err
	}
	opts := s.caddyOptions()
	if err := s.mailSetupCaddy(ctx, &opts, all); err != nil {
		return err
	}
	cfg, err := caddy.Build(opts, sites, redirects...)
	if err != nil {
		return err
	}
	if err := s.web.Load(ctx, cfg); err != nil {
		return err
	}
	s.removeOrphanSites(ctx)
	return nil
}

// removeOrphanSites deletes the files of sites that no longer exist, e.g.
// after a removal whose web server update failed. It runs only after a
// successful sync, so the web server no longer serves them, and under
// syncMu.
func (s *Service) removeOrphanSites(ctx context.Context) {
	names, err := s.deploys.Sites()
	if err != nil {
		slog.Error("listing site files failed", "err", err)
		return
	}
	for _, name := range names {
		if store.ValidateName(name) != nil {
			continue // not a site's directory
		}
		if _, err := s.store.Site(ctx, name); !errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err := s.deploys.Remove(name); err != nil {
			slog.Error("removing the files of a deleted site failed", "site", name, "err", err)
			continue
		}
		slog.Info("removed the files of a deleted site", "site", name)
	}
}

// syncSaved syncs after a change that is already saved. On failure the
// background loop will retry; the caller is still told.
func (s *Service) syncSaved(ctx context.Context) error {
	err := s.sync(ctx)
	if err == nil {
		return nil
	}
	slog.Error("sync failed", "err", err)
	return &Error{
		Kind: NotSynced,
		Msg:  "saved, but the web server could not be updated yet: " + err.Error(),
		Err:  err,
	}
}

// Run syncs once, then every interval until ctx is done, so drift (e.g. a
// restarted Caddy) heals by itself. While out of sync it retries sooner.
// The mail service is kept in sync next to it, independently (runMail).
//
// At startup the web server may not be listening yet: systemd starts Caddy
// first, but Caddy opens its admin socket a moment after it counts as
// started. Until the first successful sync, for up to startupGrace, Run
// retries often and quietly; only then does it warn.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	go s.runMail(ctx, interval)
	healthy := true
	starting := true
	graceEnd := time.Now().Add(s.startupGrace)
	for {
		err := s.Sync(ctx, System)
		if err == nil || !time.Now().Before(graceEnd) {
			starting = false
		}
		switch {
		case err != nil && ctx.Err() == nil && starting:
			slog.Debug("web server not ready yet", "err", err)
		case err != nil && ctx.Err() == nil:
			slog.Warn("web server not in sync", "err", err)
			healthy = false
		case err == nil && !healthy:
			slog.Info("web server back in sync")
			healthy = true
		}
		wait := interval
		switch {
		case starting:
			wait = s.startupRetry
		case !healthy:
			wait = s.unhealthyRetry
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
