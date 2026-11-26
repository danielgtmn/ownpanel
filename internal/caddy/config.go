// Package caddy turns desired state into a complete Caddy JSON config and
// loads it through Caddy's admin API.
//
// The whole config is rebuilt and loaded on every change instead of patching
// individual routes. Caddy applies a load gracefully (no dropped connections)
// and ignores it when nothing changed, which makes reconciling idempotent.
package caddy

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
)

// TLS modes.
const (
	TLSOff      = "off"      // plain HTTP only (local development)
	TLSInternal = "internal" // certificates from Caddy's built-in CA (testing)
	TLSACME     = "acme"     // public certificates via ACME (production)
)

// Options are the server-wide settings of the generated config.
type Options struct {
	AdminListen string // admin.listen (see Admin.Listen), kept stable across loads
	StorageDir  string // where Caddy keeps certificates and keys
	TLSMode     string
	HTTPPort    int
	HTTPSPort   int
	ACMEEmail   string // optional contact address for the certificate authority

	// PanelDomain, when set, is proxied to PanelUpstream (the daemon's own
	// host:port), so the panel gets a certificate like any site.
	PanelDomain   string
	PanelUpstream string

	// PanelIPs are the server's own addresses (IPv4/IPv6, without brackets).
	// Requests whose Host is one of them reach the panel directly, with a
	// certificate from Caddy's internal CA (browsers warn once): that is how
	// a fresh install is opened before it has a domain. Clients connecting to
	// a bare address send no SNI; Caddy then picks the certificate of the
	// local address, and falls back to the first public IPv4 (default_sni).
	PanelIPs []string
	// IPRedirect makes the PanelIPs routes redirect to https://PanelDomain
	// instead of serving the panel (once set up with a domain).
	IPRedirect bool

	// MailHost, when set, is the mail server's name: plain HTTP requests
	// for it below /.well-known/acme-challenge/ are passed to the panel,
	// which obtains the mail server's certificate itself. The route goes
	// into the server on the HTTP port only, never into a host matcher on
	// the HTTPS port, so Caddy does not request a certificate for it.
	MailHost string

	// MailSetupHosts are names like autoconfig.<domain> and
	// autodiscover.<domain> that point to this server: the paths where
	// mail programs look for their settings (MailSetupPaths) are passed to
	// the panel, everything else answers 404. They get certificates like
	// sites, so only names known to point here belong in it. They are
	// matched before domain redirects and sites.
	MailSetupHosts []string
	// MailAutoconfigDomains are the active mail domains: when one of them
	// is a site's domain, MailAutoconfigWellKnown is passed to the panel
	// before the site's files.
	MailAutoconfigDomains []string
}

// acmeChallengePath is where ACME HTTP-01 challenges are answered.
const acmeChallengePath = "/.well-known/acme-challenge/*"

// Where mail programs look for their settings.
const (
	MailAutoconfigPath      = "/mail/config-v1.1.xml"
	MailAutoconfigWellKnown = "/.well-known/autoconfig/mail/config-v1.1.xml"
	MailAutodiscoverPath    = "/autodiscover/autodiscover.xml"
)

// MailSetupPaths are the paths of MailSetupHosts passed to the panel.
var MailSetupPaths = []string{MailAutoconfigPath, MailAutoconfigWellKnown, MailAutodiscoverPath}

// Site is what the web server needs to know about one site.
type Site struct {
	Name    string
	Domains []string
	Root    string // absolute path served as web root
	Live    bool   // false until the first deployment exists
}

type obj = map[string]any

// Build returns the full Caddy config for the given sites and redirects
// (see Redirect; path redirects are only served for domains of a site).
func Build(opts Options, sites []Site, redirects ...Redirect) (obj, error) {
	switch opts.TLSMode {
	case TLSOff, TLSInternal, TLSACME:
	default:
		return nil, fmt.Errorf("unknown TLS mode %q", opts.TLSMode)
	}

	sorted := append([]Site(nil), sites...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	if len(opts.PanelIPs) > 0 && opts.PanelUpstream == "" {
		return nil, fmt.Errorf("access by IP address needs the panel's address")
	}
	ips, err := canonicalIPs(opts.PanelIPs)
	if err != nil {
		return nil, err
	}
	redirect := opts.IPRedirect && opts.PanelDomain != ""
	domainRedirects, pathRedirects, err := splitRedirects(redirects)
	if err != nil {
		return nil, err
	}

	routes := []obj{}
	if opts.PanelDomain != "" {
		if opts.PanelUpstream == "" {
			return nil, fmt.Errorf("panel domain %s needs the panel's address", opts.PanelDomain)
		}
		// reverse_proxy sets X-Forwarded-For/-Proto and has no body size
		// limit; ownpaneld enforces its own (512 MB uploads).
		routes = append(routes, obj{
			"match":    []obj{{"host": []string{opts.PanelDomain}}},
			"handle":   []obj{proxyTo(opts.PanelUpstream)},
			"terminal": true,
		})
	}
	if len(ips) > 0 {
		// Caddy's host matcher ignores the port and the brackets of IPv6
		// addresses, so the plain address matches "[2001:db8::1]:443" too.
		h := proxyTo(opts.PanelUpstream)
		if redirect {
			h = redirectTo(opts.panelURL() + "{http.request.uri}")
		}
		routes = append(routes, obj{
			"match":    []obj{{"host": ips}},
			"handle":   []obj{h},
			"terminal": true,
		})
	}
	// The setup names come before domain redirects and sites: the panel
	// lists only names that nobody but the mail domain's owner claimed,
	// so another person's redirect or site must not answer them.
	if len(opts.MailSetupHosts) > 0 {
		if opts.PanelUpstream == "" {
			return nil, fmt.Errorf("names for setting up mail programs need the panel's address")
		}
		hosts := append([]string(nil), opts.MailSetupHosts...)
		sort.Strings(hosts)
		routes = append(routes, obj{
			"match": []obj{{"host": hosts}},
			"handle": []obj{{"handler": "subroute", "routes": []obj{
				{"match": []obj{{"path": MailSetupPaths}}, "handle": []obj{proxyTo(opts.PanelUpstream)}},
				{"handle": []obj{{"handler": "static_response", "status_code": 404, "body": "Not found.\n"}}},
			}}},
			"terminal": true,
		})
	}
	routes = append(routes, domainRedirects...)
	mailDomains := map[string]bool{}
	for _, d := range opts.MailAutoconfigDomains {
		mailDomains[d] = true
	}
	for _, s := range sorted {
		if len(s.Domains) == 0 {
			continue
		}
		var siteRoutes []obj
		var autoconfig []string
		for _, d := range s.Domains {
			if mailDomains[d] {
				autoconfig = append(autoconfig, d)
			}
		}
		if len(autoconfig) > 0 {
			if opts.PanelUpstream == "" {
				return nil, fmt.Errorf("settings for mail programs need the panel's address")
			}
			siteRoutes = append(siteRoutes, obj{
				"match":    []obj{{"host": autoconfig, "path": []string{MailAutoconfigWellKnown}}},
				"handle":   []obj{proxyTo(opts.PanelUpstream)},
				"terminal": true,
			})
		}
		var handlers []obj
		if s.Live {
			handlers = []obj{
				{"handler": "vars", "root": s.Root},
				{"handler": "encode", "encodings": obj{"zstd": obj{}, "gzip": obj{}}},
				{"handler": "file_server"},
			}
		} else {
			handlers = []obj{{
				"handler":     "static_response",
				"status_code": 503,
				"body":        "This site has no content yet.\n",
			}}
		}
		routes = append(routes, obj{
			"match":    []obj{{"host": s.Domains}},
			"handle":   []obj{{"handler": "subroute", "routes": append(append(siteRoutes, sitePathRedirects(pathRedirects, s.Domains)...), obj{"handle": handlers})}},
			"terminal": true,
		})
	}
	// Requests for hosts nobody claimed.
	routes = append(routes, obj{
		"handle": []obj{{"handler": "static_response", "status_code": 404, "body": "Unknown site.\n"}},
	})

	var mailRoute obj
	if opts.MailHost != "" {
		if opts.PanelUpstream == "" {
			return nil, fmt.Errorf("mail host %s needs the panel's address", opts.MailHost)
		}
		mailRoute = obj{
			"match":    []obj{{"host": []string{opts.MailHost}, "path": []string{acmeChallengePath}}},
			"handle":   []obj{proxyTo(opts.PanelUpstream)},
			"terminal": true,
		}
	}

	server := withTimeouts(obj{"routes": routes})
	servers := obj{"sites": server}
	if opts.TLSMode == TLSOff {
		server["listen"] = []string{fmt.Sprintf(":%d", opts.HTTPPort)}
		server["automatic_https"] = obj{"disable": true}
		if mailRoute != nil {
			server["routes"] = append([]obj{mailRoute}, routes...)
		}
	} else {
		server["listen"] = []string{fmt.Sprintf(":%d", opts.HTTPSPort)}
		if len(ips) > 0 {
			server["tls_connection_policies"] = []obj{{"default_sni": defaultSNI(ips)}}
			// Caddy's automatic HTTP->HTTPS redirect drops a non-standard
			// port and writes IPv6 addresses without brackets, so the
			// addresses get their own redirects. Caddy still adds its
			// redirects for domains to this server, after these routes.
			var redirects []obj
			for _, ip := range ips {
				to := opts.panelURL()
				if !redirect {
					to = "https://" + hostPort(bracket(ip), opts.HTTPSPort, 443)
				}
				redirects = append(redirects, obj{
					"match":    []obj{{"host": []string{ip}}},
					"handle":   []obj{redirectTo(to + "{http.request.uri}")},
					"terminal": true,
				})
			}
			servers["ip_http"] = withTimeouts(obj{"listen": []string{fmt.Sprintf(":%d", opts.HTTPPort)}, "routes": redirects})
		}
		// One server on the HTTP port: the mail host's challenges go first
		// into the existing one, or into one of their own. Caddy adds its
		// HTTP->HTTPS redirects (and answers its own challenges) there too.
		if mailRoute != nil {
			if httpServer, ok := servers["ip_http"].(obj); ok {
				httpServer["routes"] = append([]obj{mailRoute}, httpServer["routes"].([]obj)...)
			} else {
				servers["mail_http"] = withTimeouts(obj{"listen": []string{fmt.Sprintf(":%d", opts.HTTPPort)}, "routes": []obj{mailRoute}})
			}
		}
	}

	apps := obj{
		"http": obj{
			"http_port":  opts.HTTPPort,
			"https_port": opts.HTTPSPort,
			"servers":    servers,
		},
	}
	var policies []obj
	if len(ips) > 0 && opts.TLSMode != TLSOff {
		// No public CA issues certificates for these addresses here; the
		// internal CA does, whatever the mode.
		policies = append(policies, obj{"subjects": ips, "issuers": []obj{{"module": "internal"}}})
	}
	switch opts.TLSMode {
	case TLSInternal:
		policies = append(policies, obj{"issuers": []obj{{"module": "internal"}}})
	case TLSACME:
		if opts.ACMEEmail != "" {
			policies = append(policies, obj{"issuers": []obj{{"module": "acme", "email": opts.ACMEEmail}}})
		}
	}
	if len(policies) > 0 {
		apps["tls"] = obj{"automation": obj{"policies": policies}}
	}
	if opts.TLSMode == TLSInternal || (len(ips) > 0 && opts.TLSMode != TLSOff) {
		// Never try to modify the system trust store from a daemon.
		apps["pki"] = obj{"certificate_authorities": obj{"local": obj{"install_trust": false}}}
	}

	return obj{
		"admin":   obj{"listen": opts.AdminListen},
		"storage": obj{"module": "file_system", "root": opts.StorageDir},
		"apps":    apps,
	}, nil
}

// Timeouts of the HTTP servers. Sites and the panel share a server, so the
// values must suit both: downloads of large files and uploads of archives
// (up to 512 MB) on slow links. The idle timeouts are reset on every read
// or write that makes progress, so only a stalled connection is cut; the
// overall read limit bounds a client that trickles just enough to never
// stall. There is deliberately no overall write limit: it would cut off
// slow downloads of large files.
const (
	ServerReadHeaderTimeout = "10s" // all request headers
	ServerReadIdleTimeout   = "1m"  // a request body that stops arriving
	ServerReadTimeout       = "1h"  // a whole request (512 MB at ~1.2 Mbit/s)
	ServerWriteIdleTimeout  = "1m"  // a client that stops reading the response
	ServerIdleTimeout       = "2m"  // a kept-alive connection between requests
)

// Timeouts between Caddy and the panel.
const (
	ProxyDialTimeout = "5s"
	// ProxyResponseHeaderTimeout counts from the moment the request,
	// including its body, has been passed on: the panel needs it to unpack
	// and publish an archive.
	ProxyResponseHeaderTimeout = "10m"
)

func withTimeouts(server obj) obj {
	server["read_header_timeout"] = ServerReadHeaderTimeout
	server["read_idle_timeout"] = ServerReadIdleTimeout
	server["read_timeout"] = ServerReadTimeout
	server["write_idle_timeout"] = ServerWriteIdleTimeout
	server["idle_timeout"] = ServerIdleTimeout
	return server
}

func proxyTo(upstream string) obj {
	return obj{
		"handler":   "reverse_proxy",
		"upstreams": []obj{{"dial": upstream}},
		"transport": obj{
			"protocol":                "http",
			"dial_timeout":            ProxyDialTimeout,
			"response_header_timeout": ProxyResponseHeaderTimeout,
		},
	}
}

func redirectTo(location string) obj {
	return obj{"handler": "static_response", "status_code": 308, "headers": obj{"Location": []string{location}}}
}

// panelURL is the panel's address on its domain, without a trailing slash.
func (o Options) panelURL() string {
	if o.TLSMode == TLSOff {
		return "http://" + hostPort(o.PanelDomain, o.HTTPPort, 80)
	}
	return "https://" + hostPort(o.PanelDomain, o.HTTPSPort, 443)
}

func hostPort(host string, port, standard int) string {
	if port == standard || port == 0 {
		return host
	}
	return host + ":" + strconv.Itoa(port)
}

func bracket(ip string) string {
	if a, err := netip.ParseAddr(ip); err == nil && a.Is6() {
		return "[" + ip + "]"
	}
	return ip
}

// canonicalIPs checks the addresses and returns them in canonical form,
// sorted (IPv4 first) and without duplicates.
func canonicalIPs(in []string) ([]string, error) {
	var addrs []netip.Addr
	for _, v := range in {
		a, err := netip.ParseAddr(v)
		if err != nil || a.Zone() != "" {
			return nil, fmt.Errorf("%q is not an IP address", v)
		}
		addrs = append(addrs, a.Unmap())
	}
	sort.Slice(addrs, func(i, j int) bool { return addrs[i].Less(addrs[j]) })
	var out []string
	for i, a := range addrs {
		if i == 0 || a != addrs[i-1] {
			out = append(out, a.String())
		}
	}
	return out, nil
}

// defaultSNI is the address whose certificate is served to clients that
// send no SNI and reached an address Caddy has no certificate for (e.g.
// behind NAT): the first public-looking IPv4, else the first public-looking
// address, else the first one.
func defaultSNI(ips []string) string {
	for _, want4 := range []bool{true, false} {
		for _, v := range ips {
			a := netip.MustParseAddr(v)
			if (!want4 || a.Is4()) && !a.IsLoopback() && !a.IsPrivate() {
				return v
			}
		}
	}
	return ips[0]
}
