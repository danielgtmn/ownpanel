package caddy

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Redirect is one redirect in the web server config.
//
// A domain redirect (Path "") answers every request for Domain. A path
// redirect answers requests for Domain whose path is Path exactly, or, for a
// Path ending in "/*", the path before the "/*" or anything below it; it is
// checked before the files of the site that has Domain.
//
// KeepPath appends the request path (domain redirects) or the part of the
// path after the prefix (prefix path redirects); KeepQuery appends the query
// string if the request has one.
type Redirect struct {
	Domain    string
	Path      string
	Target    string // absolute URL, or a path starting with "/"
	Code      int    // 301, 302, 307 or 308
	KeepPath  bool
	KeepQuery bool
}

// Caddy placeholders used in Location headers. prefixed_query is "?query",
// or "" when the request has no query string (no lonely "?").
const (
	phPath          = "{http.request.uri.path}"
	phPrefixedQuery = "{http.request.uri.prefixed_query}"
	// regexpName names the path_regexp matcher of path redirects; its first
	// capture group is the rest of the path after a prefix.
	regexpName = "redirect"
	phRest     = "{http.regexp." + regexpName + ".1}"
)

// IsPrefix reports whether a redirect path is the prefix form "/x/*".
func (r Redirect) IsPrefix() bool { return strings.HasSuffix(r.Path, "/*") }

func (r Redirect) check() error {
	switch r.Code {
	case 301, 302, 307, 308:
	default:
		return fmt.Errorf("redirect %s%s: status code %d is not a redirect", r.Domain, r.Path, r.Code)
	}
	// Braces would be read as placeholders.
	if strings.ContainsAny(r.Target, "{}") || strings.ContainsAny(r.Path, "{}") {
		return fmt.Errorf("redirect %s%s: braces are not allowed", r.Domain, r.Path)
	}
	if r.Domain == "" || r.Target == "" {
		return fmt.Errorf("redirect %s%s: domain and target are required", r.Domain, r.Path)
	}
	if r.Path != "" && !strings.HasPrefix(r.Path, "/") {
		return fmt.Errorf("redirect %s%s: the path must start with /", r.Domain, r.Path)
	}
	if strings.Contains(strings.TrimSuffix(r.Path, "/*"), "*") {
		return fmt.Errorf("redirect %s%s: * is only allowed as /* at the end", r.Domain, r.Path)
	}
	return nil
}

// withQuery appends the request's query string if asked to.
func (r Redirect) withQuery(loc string) string {
	if r.KeepQuery {
		return loc + phPrefixedQuery
	}
	return loc
}

func redirectHandler(code int, location string) obj {
	return obj{"handler": "static_response", "status_code": code, "headers": obj{"Location": []string{location}}}
}

// domainRedirectRoute answers every request for the domain.
func domainRedirectRoute(r Redirect) obj {
	loc := r.Target
	if r.KeepPath {
		loc = strings.TrimSuffix(loc, "/") + phPath
	}
	return obj{
		"match":    []obj{{"host": []string{r.Domain}}},
		"handle":   []obj{redirectHandler(r.Code, r.withQuery(loc))},
		"terminal": true,
	}
}

// pathRedirectRoutes are the routes of one path redirect, to be placed in
// the site's subroute before its files. They match the path exactly (case
// sensitive, like the files on disk) with a regular expression.
func pathRedirectRoutes(r Redirect) []obj {
	route := func(pattern, loc string) obj {
		return obj{
			"match": []obj{{
				"host":        []string{r.Domain},
				"path_regexp": obj{"name": regexpName, "pattern": pattern},
			}},
			"handle":   []obj{redirectHandler(r.Code, r.withQuery(loc))},
			"terminal": true,
		}
	}
	if !r.IsPrefix() {
		return []obj{route("^"+regexp.QuoteMeta(r.Path)+"$", r.Target)}
	}
	base := strings.TrimSuffix(r.Path, "/*") // "" for "/*"
	var out []obj
	if base != "" {
		out = append(out, route("^"+regexp.QuoteMeta(base)+"$", r.Target))
	}
	loc := r.Target
	if r.KeepPath {
		// The rest never starts with "/": "/+" takes every slash after the
		// prefix, so a target "/" cannot turn into "//host" (another site).
		if !strings.HasSuffix(loc, "/") {
			loc += "/"
		}
		loc += phRest
	}
	return append(out, route("^"+regexp.QuoteMeta(base)+"/+(.*)$", loc))
}

// splitRedirects checks the redirects and returns the routes of the domain
// redirects (sorted by domain) and the path redirects by domain, in the
// order they must be tried: exact paths first, then longer prefixes first.
func splitRedirects(redirects []Redirect) ([]obj, map[string][]Redirect, error) {
	var domain []Redirect
	paths := map[string][]Redirect{}
	for _, r := range redirects {
		if err := r.check(); err != nil {
			return nil, nil, err
		}
		if r.Path == "" {
			domain = append(domain, r)
		} else {
			paths[r.Domain] = append(paths[r.Domain], r)
		}
	}
	sort.Slice(domain, func(i, j int) bool { return domain[i].Domain < domain[j].Domain })
	var routes []obj
	for i, r := range domain {
		if i > 0 && domain[i-1].Domain == r.Domain {
			return nil, nil, fmt.Errorf("the domain %s redirects twice", r.Domain)
		}
		routes = append(routes, domainRedirectRoute(r))
	}
	for _, list := range paths {
		sort.SliceStable(list, func(i, j int) bool {
			a, b := list[i], list[j]
			if a.IsPrefix() != b.IsPrefix() {
				return !a.IsPrefix()
			}
			if len(a.Path) != len(b.Path) {
				return len(a.Path) > len(b.Path)
			}
			return a.Path < b.Path
		})
	}
	return routes, paths, nil
}

// sitePathRedirects are the path redirect routes for the domains of a site.
func sitePathRedirects(paths map[string][]Redirect, domains []string) []obj {
	var out []obj
	for _, d := range domains {
		for _, r := range paths[d] {
			out = append(out, pathRedirectRoutes(r)...)
		}
	}
	return out
}
