package panel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/dns"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// Previews: every kept version of a site can be opened on its own address,
// <label>.<preview domain>, whether it is live or not. The label and the key
// that the preview link carries are derived from the site name and the
// version id with a secret of this panel (keyPreviewSecret), so they need
// no storage, cannot be guessed, and only ever map to versions that exist:
// the web server config lists one route per kept version, and a host name
// is never turned into a path.
//
// The host name alone is not the secret. With public certificates every
// opened preview's name appears in the certificate logs, which anyone can
// read; the files need the key (see caddy.Preview).

// keyPreviewDomain and keyPreviewSecret are keys of the settings table.
const (
	keyPreviewDomain = "preview_domain"
	keyPreviewSecret = "preview_secret"
)

// previewSecretPurpose seals the preview secret; part of the stored format,
// never change it.
const previewSecretPurpose = "ownpanel preview secret"

// PreviewPermissionPath is where the web server asks whether it may obtain a
// certificate for a host name below the preview domain (?domain=<name>).
const PreviewPermissionPath = "/api/previews/certificate-allowed"

// Lengths of the label and the key, in base32 characters: 130 and 160 bits.
const (
	previewLabelLen = 26
	previewKeyLen   = 32
)

// previewCheckLabel is looked up below the preview domain to see whether
// the wildcard record points here.
const previewCheckLabel = "ownpanel-check"

var previewEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// initPreviewSecret loads the preview secret, creating it on first start.
// Without a key file (tests) it is made up for this run only.
func (s *Service) initPreviewSecret(ctx context.Context) error {
	m, err := s.store.Settings(ctx)
	if err != nil {
		return err
	}
	if s.secrets == nil {
		s.previewSecret = randomBytes(32)
		return nil
	}
	if v := m[keyPreviewSecret]; v != "" {
		sealed, err := base64.StdEncoding.DecodeString(v)
		if err == nil {
			if secret, err := s.secrets.Open(sealed, previewSecretPurpose); err == nil && len(secret) == 32 {
				s.previewSecret = secret
				return nil
			}
		}
		// A database restored without its key file: the old preview
		// addresses cannot be recovered, new ones replace them.
		slog.Warn("the stored preview secret cannot be read; preview addresses change")
	}
	secret := randomBytes(32)
	sealed := s.secrets.Seal(secret, previewSecretPurpose)
	if err := s.store.PutSettings(ctx, map[string]string{keyPreviewSecret: base64.StdEncoding.EncodeToString(sealed)}); err != nil {
		return err
	}
	s.previewSecret = secret
	return nil
}

func (s *Service) previewMAC(kind, site, release string, n int) string {
	h := hmac.New(sha256.New, s.previewSecret)
	h.Write([]byte(kind + "\x00" + site + "\x00" + release))
	return previewEncoding.EncodeToString(h.Sum(nil))[:n]
}

// previewLabel is the DNS label of a version's preview: lowercase letters
// and digits only.
func (s *Service) previewLabel(site, release string) string {
	return s.previewMAC("label", site, release, previewLabelLen)
}

func (s *Service) previewKey(site, release string) string {
	return s.previewMAC("key", site, release, previewKeyLen)
}

// PreviewDomain is the domain below which previews are served; "" means
// previews have no addresses.
func (s *Service) PreviewDomain() string {
	return s.state().previewDomain
}

// previewsServed: previews get routes. With TLS, the web server must be
// able to ask the panel before it obtains a certificate.
func (s *Service) previewsServed() bool {
	return s.PreviewDomain() != "" && (s.opts.PanelUpstream != "" || s.opts.TLSMode == caddy.TLSOff)
}

// previewURL is the link to the preview of a version, "" if previews have
// no addresses.
func (s *Service) previewURL(site, release string) string {
	if !s.previewsServed() {
		return ""
	}
	host := s.previewLabel(site, release) + "." + s.PreviewDomain()
	scheme, port, standard := "https", s.opts.HTTPSPort, 443
	if s.opts.TLSMode == caddy.TLSOff {
		scheme, port, standard = "http", s.opts.HTTPPort, 80
	}
	if port != standard && port != 0 {
		host += ":" + strconv.Itoa(port)
	}
	return fmt.Sprintf("%s://%s/?%s=%s", scheme, host, caddy.PreviewQuery, s.previewKey(site, release))
}

// caddyPreviews are the previews of one site for the web server config.
func (s *Service) caddyPreviews(site string) []caddy.Preview {
	if !s.previewsServed() {
		return nil
	}
	var out []caddy.Preview
	for _, r := range s.deploys.Releases(site) {
		out = append(out, caddy.Preview{Label: s.previewLabel(site, r), Root: s.deploys.ReleaseDir(site, r), Key: s.previewKey(site, r)})
	}
	return out
}

// PreviewCertificateAllowed tells the web server whether host is the
// address of a kept version, so that it may obtain a certificate for it.
// It answers nothing else: host names that are not previews, the preview
// domain itself and deeper names are all refused alike.
func (s *Service) PreviewCertificateAllowed(ctx context.Context, host string) (bool, error) {
	pd := s.PreviewDomain()
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	label, ok := strings.CutSuffix(host, "."+pd)
	if pd == "" || !ok || len(label) != previewLabelLen || strings.Trim(label, "abcdefghijklmnopqrstuvwxyz234567") != "" {
		return false, nil
	}
	sites, err := s.store.Sites(ctx)
	if err != nil {
		return false, storeError(err)
	}
	for _, site := range sites {
		for _, r := range s.deploys.Releases(site.Name) {
			if hmac.Equal([]byte(s.previewLabel(site.Name, r)), []byte(label)) {
				return true, nil
			}
		}
	}
	return false, nil
}

// underPreviewDomain reports whether d is a name below the preview domain,
// which the previews take for themselves.
func (s *Service) underPreviewDomain(d string) bool {
	pd := s.PreviewDomain()
	return pd != "" && strings.HasSuffix(d, "."+pd)
}

func errUnderPreviewDomain(d, pd string) error {
	return errorf(Invalid, "the domain %s is below %s, where previews of versions are served", d, pd)
}

// validPreviewDomain normalizes a preview domain: a plain host name with a
// dot, not an address, and no domain in use may be below it.
func (s *Service) validPreviewDomain(ctx context.Context, domain string) (string, error) {
	if _, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(domain), "[]")); err == nil {
		return "", errorf(Invalid, "previews need a domain name like preview.example.com, not an IP address")
	}
	d, err := store.NormalizeDomain(domain)
	labels := strings.Split(d, ".")
	if err != nil || strings.Trim(labels[len(labels)-1], "0123456789") == "" {
		return "", errorf(Invalid, "%q is not a valid domain name; enter one like preview.example.com", strings.TrimSpace(domain))
	}
	below := func(x string) bool { return x != "" && strings.HasSuffix(x, "."+d) }
	if below(s.PanelDomain()) {
		return "", errorf(Conflict, "the panel's own address %s is below %s; previews need a domain of their own", s.PanelDomain(), d)
	}
	if below(s.MailHost()) {
		return "", errorf(Conflict, "the mail server's name %s is below %s; previews need a domain of their own", s.MailHost(), d)
	}
	sites, err := s.store.Sites(ctx)
	if err != nil {
		return "", storeError(err)
	}
	for _, site := range sites {
		for _, sd := range site.Domains {
			if below(sd) {
				return "", errorf(Conflict, "the site %q uses %s, which is below %s; previews need a domain of their own", site.Name, sd, d)
			}
		}
	}
	redirects, err := s.store.Redirects(ctx)
	if err != nil {
		return "", storeError(err)
	}
	for _, r := range redirects {
		if below(r.SourceDomain) {
			return "", errorf(Conflict, "a redirect uses %s, which is below %s; previews need a domain of their own", r.SourceDomain, d)
		}
	}
	return d, nil
}

// PreviewDNS tells whether the names below the preview domain point at this
// server, and which of the admin's zones holds them.
type PreviewDNS struct {
	// Name is the record to create: "*.<preview domain>".
	Name     string      `json:"name"`
	State    string      `json:"state"`
	Found    []string    `json:"found"`
	Expected []string    `json:"expected"`
	Zone     *DomainZone `json:"zone,omitempty"`
}

// previewDNS checks a name below the preview domain. Only for admins (the
// caller checks).
func (s *Service) previewDNS(ctx context.Context, a Actor) PreviewDNS {
	pd := s.PreviewDomain()
	out := PreviewDNS{State: CheckNotConfigured, Found: []string{}, Expected: ipStrings(s.effectiveServerIPs())}
	if pd == "" {
		return out
	}
	out.Name = "*." + pd
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		defer cancel()
		c := dns.CheckDomain(lctx, s.resolver, previewCheckLabel+"."+pd, s.effectiveServerIPs())
		out.State = stateName(c.State)
		if c.Found != nil {
			out.Found = c.Found
		}
	}()
	if conns, err := s.ownConnections(ctx, a); err == nil {
		if m, _ := s.zoneMatches(ctx, conns, out.Name); len(m) == 1 && !m[0].zone.ReadOnly {
			out.Zone = &DomainZone{ConnectionID: m[0].conn.ID, ConnectionName: m[0].conn.Name, Zone: m[0].zone.Name, RecordName: m[0].name}
		}
	}
	wg.Wait()
	return out
}

// PointPreviewDomain makes all names below the preview domain point at this
// server: a wildcard record "*.<preview domain>" in the admin's zone that
// holds it, written like PointDomain does. Admins only.
func (s *Service) PointPreviewDomain(ctx context.Context, a Actor) (PointResult, error) {
	if err := needAdmin(a); err != nil {
		return PointResult{}, err
	}
	pd := s.PreviewDomain()
	if pd == "" {
		return PointResult{}, errorf(Conflict, "no domain for previews is set")
	}
	if err := s.needServerIPs(); err != nil {
		return PointResult{}, err
	}
	return s.pointDomain(ctx, a, "*."+pd)
}

// versionsView lists the kept versions of a site, newest first.
func (s *Service) versionsView(site string) []Version {
	releases := s.deploys.Releases(site)
	cur := s.deploys.Current(site)
	out := make([]Version, 0, len(releases))
	for _, r := range slices.Backward(releases) {
		out = append(out, Version{ID: r, Live: r == cur, Pending: s.deploys.Pending(site, r), PreviewURL: s.previewURL(site, r)})
	}
	return out
}
