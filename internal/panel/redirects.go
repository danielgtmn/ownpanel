package panel

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// Redirects: a domain redirect answers every request for a domain that no
// site has (www.example.com -> https://example.com); a path redirect answers
// requests for one path or path prefix of a site's domain before the site's
// files are served (/old -> /new, /blog/* -> https://blog.example.com).
//
// Who may do what:
//   - domain redirects belong to the person who created them; they and
//     admins manage them. Tokens limited to sites cannot see or manage them.
//   - path redirects belong to their site: whoever may change the site
//     manages them, including tokens limited to that site.
//   - read-only tokens see what their owner sees.

// Redirect limits and rules.
const (
	// MaxRedirectsPerUser counts domain redirects of a person and path
	// redirects of the sites they own.
	MaxRedirectsPerUser = 500
	maxRedirectPath     = 1024
	maxRedirectTarget   = 2048
)

// RedirectCodes are the status codes a redirect can answer with.
var RedirectCodes = []int{301, 302, 307, 308}

// Redirect is what callers see of a redirect. SourcePath "" is a domain
// redirect; otherwise an exact path ("/old") or a prefix ("/blog/*").
type Redirect struct {
	ID           int64     `json:"id"`
	Owner        string    `json:"owner"`
	SourceDomain string    `json:"source_domain"`
	SourcePath   string    `json:"source_path"`
	Target       string    `json:"target"`
	Code         int       `json:"code"`
	KeepPath     bool      `json:"keep_path"`
	KeepQuery    bool      `json:"keep_query"`
	CreatedAt    time.Time `json:"created_at"`
}

// RedirectInput is a redirect to create, or the new state of one.
type RedirectInput struct {
	SourceDomain string
	SourcePath   string
	Target       string
	Code         int
	KeepPath     bool
	KeepQuery    bool
}

func redirectView(r store.Redirect) Redirect {
	return Redirect{
		ID: r.ID, Owner: r.Owner, SourceDomain: r.SourceDomain, SourcePath: r.SourcePath, Target: r.Target,
		Code: r.Code, KeepPath: r.KeepPath, KeepQuery: r.KeepQuery, CreatedAt: r.CreatedAt,
	}
}

// redirectSource names a redirect in messages and the activity log.
func redirectSource(domain, path string) string { return domain + path }

// ---- permissions ----

// sitesByName returns every site by name.
func (s *Service) sitesByName(ctx context.Context) (map[string]store.Site, error) {
	all, err := s.store.Sites(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	out := make(map[string]store.Site, len(all))
	for _, site := range all {
		out[site.Name] = site
	}
	return out, nil
}

// canSeeRedirect: path redirects are visible with their site; domain
// redirects to their owner and admins, never to tokens limited to sites.
func (a Actor) canSeeRedirect(r store.Redirect, sites map[string]store.Site) bool {
	if !a.authenticated() {
		return false
	}
	if r.SiteName != "" {
		site, ok := sites[r.SiteName]
		return ok && a.canSee(site)
	}
	return a.Sites == nil && (a.Admin || (a.UserID != 0 && r.OwnerID == a.UserID))
}

// needRedirectWrite: changing redirects needs write access and a person.
func needRedirectWrite(a Actor) error {
	if err := needAuth(a); err != nil {
		return err
	}
	if err := needWrite(a); err != nil {
		return err
	}
	if a.Via == ViaSystem || a.UserID == 0 {
		return errorf(Forbidden, "redirects must be changed by a person")
	}
	return nil
}

// visibleRedirect returns a redirect the actor may see; others answer
// NotFound like missing ones.
func (s *Service) visibleRedirect(ctx context.Context, a Actor, id int64) (store.Redirect, map[string]store.Site, error) {
	if err := needAuth(a); err != nil {
		return store.Redirect{}, nil, err
	}
	r, err := s.store.Redirect(ctx, id)
	if err != nil {
		return store.Redirect{}, nil, storeError(err)
	}
	sites, err := s.sitesByName(ctx)
	if err != nil {
		return store.Redirect{}, nil, err
	}
	if !a.canSeeRedirect(r, sites) {
		return store.Redirect{}, nil, errorf(NotFound, "redirect not found")
	}
	return r, sites, nil
}

// ---- validation ----

// redirectDomain normalizes a source domain: a host name, not an address.
func redirectDomain(domain string) (string, error) {
	raw := strings.TrimSpace(domain)
	if raw == "" {
		return "", errorf(Invalid, "enter the domain to redirect from")
	}
	if _, err := netip.ParseAddr(strings.Trim(raw, "[]")); err == nil {
		return "", errorf(Invalid, "a redirect needs a domain name like www.example.com, not an IP address")
	}
	d, err := store.NormalizeDomain(raw)
	if err != nil {
		return "", errorf(Invalid, "%q is not a valid domain name; enter one like www.example.com", raw)
	}
	labels := strings.Split(d, ".")
	if _, err := strconv.Atoi(labels[len(labels)-1]); err == nil {
		return "", errorf(Invalid, "a redirect needs a domain name like www.example.com, not an IP address")
	}
	return d, nil
}

func badChar(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r) || r == '{' || r == '}'
}

// redirectPath checks a source path: "" (whole domain), an exact path
// "/old", or a prefix "/blog/*" ("/*" is every path).
func redirectPath(path string) (string, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", nil
	}
	switch {
	case !strings.HasPrefix(p, "/"):
		return "", errorf(Invalid, "the path %q must start with /", p)
	case len(p) > maxRedirectPath:
		return "", errorf(Invalid, "the path can be at most %d characters long", maxRedirectPath)
	case !utf8.ValidString(p) || strings.ContainsFunc(p, badChar):
		return "", errorf(Invalid, "the path %q must not contain spaces, line breaks or braces", p)
	case strings.ContainsAny(p, "?#"):
		return "", errorf(Invalid, "the path %q must not contain ? or #; a redirect applies to every query string", p)
	case strings.Contains(p, "%"):
		return "", errorf(Invalid, "enter the path %q without percent-encoding (%%20 is a space, which is not allowed)", p)
	case strings.Contains(strings.TrimSuffix(p, "/*"), "*"):
		return "", errorf(Invalid, "the path %q can only end in /* (for everything below a path); * is not allowed elsewhere", p)
	}
	base := strings.TrimSuffix(p, "/*")
	for _, seg := range strings.Split(base, "/")[1:] {
		if seg == "." || seg == ".." {
			return "", errorf(Invalid, "the path %q must not contain . or .. segments", p)
		}
	}
	if strings.Contains(base, "//") {
		return "", errorf(Invalid, "the path %q must not contain //", p)
	}
	return p, nil
}

// redirectTarget checks a target: an absolute http(s) address, or for path
// redirects also a path on the same domain. It returns the target and the
// host and path it leads to ("" host: the source domain).
func redirectTarget(target string, pathRedirect bool) (string, string, string, error) {
	t := strings.TrimSpace(target)
	switch {
	case t == "":
		return "", "", "", errorf(Invalid, "enter where the redirect should lead")
	case len(t) > maxRedirectTarget:
		return "", "", "", errorf(Invalid, "the target can be at most %d characters long", maxRedirectTarget)
	case !utf8.ValidString(t) || strings.ContainsFunc(t, badChar):
		return "", "", "", errorf(Invalid, "the target must not contain spaces, line breaks or braces")
	}
	if strings.HasPrefix(t, "/") {
		if !pathRedirect {
			return "", "", "", errorf(Invalid, "a whole domain must redirect to a full address like https://example.com, not to a path")
		}
		if strings.HasPrefix(t, "//") {
			return "", "", "", errorf(Invalid, "a target path must not start with //; enter a full address like https://example.com instead")
		}
		u, err := url.Parse(t)
		if err != nil {
			return "", "", "", errorf(Invalid, "%q is not a valid path", t)
		}
		return t, "", u.Path, nil
	}
	u, err := url.Parse(t)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Opaque != "" {
		if pathRedirect {
			return "", "", "", errorf(Invalid, "%q is not a valid target: enter a full address starting with https:// or http://, or a path starting with /", t)
		}
		return "", "", "", errorf(Invalid, "%q is not a valid target: enter a full address starting with https:// or http://", t)
	}
	if u.User != nil {
		return "", "", "", errorf(Invalid, "the target must not contain a user name or password")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", "", "", errorf(Invalid, "%q has no host name", t)
	}
	if p := u.Port(); p != "" && !(u.Scheme == "https" && p == "443") && !(u.Scheme == "http" && p == "80") {
		host += ":" + p // another port is another place
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	return t, host, path, nil
}

// validRedirect checks and normalizes a redirect. It does not look at sites
// or other redirects.
func (s *Service) validRedirect(in RedirectInput) (store.Redirect, error) {
	d, err := redirectDomain(in.SourceDomain)
	if err != nil {
		return store.Redirect{}, err
	}
	if pd := s.PanelDomain(); pd != "" && d == pd {
		return store.Redirect{}, errorf(Invalid, "the domain %s is the address of the panel itself", d)
	}
	p, err := redirectPath(in.SourcePath)
	if err != nil {
		return store.Redirect{}, err
	}
	t, host, tpath, err := redirectTarget(in.Target, p != "")
	if err != nil {
		return store.Redirect{}, err
	}
	if !slices.Contains(RedirectCodes, in.Code) {
		return store.Redirect{}, errorf(Invalid, "the status code must be 301, 302, 307 or 308, not %d", in.Code)
	}
	if strings.ContainsAny(t, "?#") && (in.KeepPath && (p == "" || strings.HasSuffix(p, "/*")) || in.KeepQuery) {
		return store.Redirect{}, errorf(Invalid, "a target with ? or # cannot keep the path or the query string; remove them from the target or turn off keeping them")
	}
	// A redirect to where it applies again would loop forever.
	if host == "" || host == d {
		base := strings.TrimSuffix(p, "/*")
		loop := false
		switch {
		case p == "":
			loop = true
		case strings.HasSuffix(p, "/*"):
			loop = base == "" || tpath == base || strings.HasPrefix(tpath, base+"/")
		default:
			loop = tpath == p
		}
		if loop {
			return store.Redirect{}, errorf(Invalid, "the target leads back to %s, which would redirect again forever", redirectSource(d, p))
		}
	}
	return store.Redirect{
		SourceDomain: d, SourcePath: p, Target: t, Code: in.Code,
		KeepPath: in.KeepPath, KeepQuery: in.KeepQuery,
	}, nil
}

// placeRedirect completes a validated redirect with its site and owner and
// checks that the actor may have it there.
func (s *Service) placeRedirect(a Actor, r *store.Redirect, sites map[string]store.Site, owner int64) error {
	if r.SourcePath == "" {
		if a.Sites != nil {
			return errorf(Forbidden, "this access token is limited to certain sites and cannot manage domain redirects")
		}
		r.SiteName, r.OwnerID = "", owner
		return nil
	}
	for _, site := range sites {
		if slices.Contains(site.Domains, r.SourceDomain) {
			if !a.canSee(site) {
				break
			}
			r.SiteName = site.Name
			return nil
		}
	}
	return errorf(Invalid, "the domain %s is not a domain of one of your sites; path redirects work only on the domains of a site", r.SourceDomain)
}

func redirectDetail(r store.Redirect) string {
	var keep []string
	if r.KeepPath {
		keep = append(keep, "path")
	}
	if r.KeepQuery {
		keep = append(keep, "query")
	}
	d := fmt.Sprintf("%d to %s", r.Code, r.Target)
	if len(keep) > 0 {
		d += ", keeping " + strings.Join(keep, " and ")
	}
	if utf8.RuneCountInString(d) > 500 {
		d = string([]rune(d)[:499]) + "…"
	}
	return d
}

// redirectStoreError explains store errors of saving a redirect, without
// naming sites the actor may not see.
func (s *Service) redirectStoreError(a Actor, err error, sites map[string]store.Site) error {
	var taken *store.DomainTakenError
	if errors.As(err, &taken) {
		if site, ok := sites[taken.Site]; ok && a.canSee(site) {
			return &Error{Kind: Invalid, Msg: fmt.Sprintf("the domain %s belongs to the site %s; to send all of it elsewhere, add a path redirect for /* on that site, or remove the domain from the site", taken.Domain, taken.Site), Err: err}
		}
		return &Error{Kind: Invalid, Msg: fmt.Sprintf("the domain %s is already used by another site", taken.Domain), Err: err}
	}
	return storeError(err)
}

// ---- operations ----

// Redirects lists the redirects the actor may see (admins: all), sorted by
// domain and path.
func (s *Service) Redirects(ctx context.Context, a Actor) ([]Redirect, error) {
	if err := needAuth(a); err != nil {
		return nil, err
	}
	all, err := s.store.Redirects(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	sites, err := s.sitesByName(ctx)
	if err != nil {
		return nil, err
	}
	out := []Redirect{}
	for _, r := range all {
		if a.canSeeRedirect(r, sites) {
			out = append(out, redirectView(r))
		}
	}
	return out, nil
}

// SiteRedirects lists the path redirects on the domains of a site.
func (s *Service) SiteRedirects(ctx context.Context, a Actor, siteName string) ([]Redirect, error) {
	site, err := s.visibleSite(ctx, a, siteName)
	if err != nil {
		return nil, err
	}
	all, err := s.store.Redirects(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	out := []Redirect{}
	for _, r := range all {
		if r.SiteName == site.Name {
			out = append(out, redirectView(r))
		}
	}
	return out, nil
}

// Redirect returns one redirect.
func (s *Service) Redirect(ctx context.Context, a Actor, id int64) (Redirect, error) {
	r, _, err := s.visibleRedirect(ctx, a, id)
	if err != nil {
		return Redirect{}, err
	}
	return redirectView(r), nil
}

// CreateRedirect adds a redirect. If only the web server update fails, it is
// saved and returned together with a NotSynced error.
func (s *Service) CreateRedirect(ctx context.Context, a Actor, in RedirectInput) (Redirect, error) {
	if err := needRedirectWrite(a); err != nil {
		return Redirect{}, err
	}
	r, err := s.validRedirect(in)
	if err != nil {
		return Redirect{}, err
	}
	sites, err := s.sitesByName(ctx)
	if err != nil {
		return Redirect{}, err
	}
	if err := s.placeRedirect(a, &r, sites, a.UserID); err != nil {
		return Redirect{}, err
	}
	saved, err := s.store.CreateRedirect(ctx, r, MaxRedirectsPerUser)
	if err != nil {
		return Redirect{}, s.redirectStoreError(a, err, sites)
	}
	s.record(ctx, a, "redirect.create", redirectSource(saved.SourceDomain, saved.SourcePath), redirectDetail(saved))
	return redirectView(saved), s.syncSaved(ctx)
}

// UpdateRedirect replaces a redirect. The actor must be allowed to manage
// both the redirect as it was and as it will be.
func (s *Service) UpdateRedirect(ctx context.Context, a Actor, id int64, in RedirectInput) (Redirect, error) {
	cur, sites, err := s.visibleRedirect(ctx, a, id)
	if err != nil {
		return Redirect{}, err
	}
	if err := needRedirectWrite(a); err != nil {
		return Redirect{}, err
	}
	if cur.SiteName == "" && a.Sites != nil {
		return Redirect{}, errorf(Forbidden, "this access token is limited to certain sites and cannot manage domain redirects")
	}
	r, err := s.validRedirect(in)
	if err != nil {
		return Redirect{}, err
	}
	owner := a.UserID
	if cur.SiteName == "" {
		owner = cur.OwnerID // an admin's change keeps the owner
	}
	if err := s.placeRedirect(a, &r, sites, owner); err != nil {
		return Redirect{}, err
	}
	r.ID = id
	saved, err := s.store.UpdateRedirect(ctx, r, MaxRedirectsPerUser)
	if err != nil {
		return Redirect{}, s.redirectStoreError(a, err, sites)
	}
	detail := redirectDetail(saved)
	if from := redirectSource(cur.SourceDomain, cur.SourcePath); from != redirectSource(saved.SourceDomain, saved.SourcePath) {
		detail = "was " + from + "; " + detail
	}
	s.record(ctx, a, "redirect.update", redirectSource(saved.SourceDomain, saved.SourcePath), detail)
	return redirectView(saved), s.syncSaved(ctx)
}

// DeleteRedirect removes a redirect.
func (s *Service) DeleteRedirect(ctx context.Context, a Actor, id int64) error {
	cur, _, err := s.visibleRedirect(ctx, a, id)
	if err != nil {
		return err
	}
	if err := needRedirectWrite(a); err != nil {
		return err
	}
	if err := s.store.DeleteRedirect(ctx, id); err != nil {
		return storeError(err)
	}
	s.record(ctx, a, "redirect.delete", redirectSource(cur.SourceDomain, cur.SourcePath), redirectDetail(cur))
	return s.syncSaved(ctx)
}

// caddyRedirects are all redirects for the web server config.
func (s *Service) caddyRedirects(ctx context.Context) ([]caddy.Redirect, error) {
	all, err := s.store.Redirects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]caddy.Redirect, 0, len(all))
	for _, r := range all {
		out = append(out, caddy.Redirect{
			Domain: r.SourceDomain, Path: r.SourcePath, Target: r.Target, Code: r.Code,
			KeepPath: r.KeepPath, KeepQuery: r.KeepQuery,
		})
	}
	return out, nil
}

// visibleDomainRedirect reports whether domain has a domain redirect the
// actor may see.
func (s *Service) visibleDomainRedirect(ctx context.Context, a Actor, domain string) (bool, error) {
	all, err := s.store.Redirects(ctx)
	if err != nil {
		return false, storeError(err)
	}
	for _, r := range all {
		if r.SourcePath == "" && r.SourceDomain == domain {
			return a.canSeeRedirect(r, nil), nil
		}
	}
	return false, nil
}
