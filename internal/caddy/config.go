// Package caddy turns desired state into a complete Caddy JSON config and
// loads it through Caddy's admin API.
//
// The whole config is rebuilt and loaded on every change instead of patching
// individual routes. Caddy applies a load gracefully (no dropped connections)
// and ignores it when nothing changed, which makes reconciling idempotent.
package caddy

import (
	"fmt"
	"sort"
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
}

// Site is what the web server needs to know about one site.
type Site struct {
	Name    string
	Domains []string
	Root    string // absolute path served as web root
	Live    bool   // false until the first deployment exists
}

type obj = map[string]any

// Build returns the full Caddy config for the given sites.
func Build(opts Options, sites []Site) (obj, error) {
	switch opts.TLSMode {
	case TLSOff, TLSInternal, TLSACME:
	default:
		return nil, fmt.Errorf("unknown TLS mode %q", opts.TLSMode)
	}

	sorted := append([]Site(nil), sites...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	routes := []obj{}
	if opts.PanelDomain != "" {
		if opts.PanelUpstream == "" {
			return nil, fmt.Errorf("panel domain %s needs the panel's address", opts.PanelDomain)
		}
		// reverse_proxy sets X-Forwarded-For/-Proto and has no body size
		// limit; ownpaneld enforces its own (512 MB uploads).
		routes = append(routes, obj{
			"match": []obj{{"host": []string{opts.PanelDomain}}},
			"handle": []obj{{
				"handler":   "reverse_proxy",
				"upstreams": []obj{{"dial": opts.PanelUpstream}},
			}},
			"terminal": true,
		})
	}
	for _, s := range sorted {
		if len(s.Domains) == 0 {
			continue
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
			"handle":   []obj{{"handler": "subroute", "routes": []obj{{"handle": handlers}}}},
			"terminal": true,
		})
	}
	// Requests for hosts nobody claimed.
	routes = append(routes, obj{
		"handle": []obj{{"handler": "static_response", "status_code": 404, "body": "Unknown site.\n"}},
	})

	server := obj{"routes": routes}
	if opts.TLSMode == TLSOff {
		server["listen"] = []string{fmt.Sprintf(":%d", opts.HTTPPort)}
		server["automatic_https"] = obj{"disable": true}
	} else {
		server["listen"] = []string{fmt.Sprintf(":%d", opts.HTTPSPort)}
	}

	apps := obj{
		"http": obj{
			"http_port":  opts.HTTPPort,
			"https_port": opts.HTTPSPort,
			"servers":    obj{"sites": server},
		},
	}
	switch opts.TLSMode {
	case TLSInternal:
		apps["tls"] = obj{"automation": obj{"policies": []obj{{"issuers": []obj{{"module": "internal"}}}}}}
		// Never try to modify the system trust store from a daemon.
		apps["pki"] = obj{"certificate_authorities": obj{"local": obj{"install_trust": false}}}
	case TLSACME:
		if opts.ACMEEmail != "" {
			apps["tls"] = obj{"automation": obj{"policies": []obj{{"issuers": []obj{{"module": "acme", "email": opts.ACMEEmail}}}}}}
		}
	}

	return obj{
		"admin":   obj{"listen": opts.AdminListen},
		"storage": obj{"module": "file_system", "root": opts.StorageDir},
		"apps":    apps,
	}, nil
}
