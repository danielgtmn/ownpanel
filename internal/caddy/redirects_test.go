package caddy

import (
	"encoding/json"
	"strings"
	"testing"
)

func redirectRoutesOf(t *testing.T, sites []Site, redirects ...Redirect) (string, []any) {
	t.Helper()
	cfg, err := Build(Options{AdminListen: "127.0.0.1:2019", StorageDir: "/data/caddy", TLSMode: TLSACME, HTTPPort: 80, HTTPSPort: 443}, sites, redirects...)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(cfg)
	var v struct {
		Apps struct {
			HTTP struct {
				Servers struct {
					Sites struct {
						Routes []any `json:"routes"`
					} `json:"sites"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	json.Unmarshal(b, &v)
	return string(b), v.Apps.HTTP.Servers.Sites.Routes
}

func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestBuildDomainRedirects(t *testing.T) {
	_, routes := redirectRoutesOf(t, []Site{{Name: "a", Domains: []string{"example.com"}, Root: "/r", Live: true}},
		Redirect{Domain: "www.example.com", Target: "https://example.com/", Code: 301, KeepPath: true, KeepQuery: true},
		Redirect{Domain: "old.example.com", Target: "https://example.com/landing", Code: 302},
		Redirect{Domain: "q.example.com", Target: "https://example.com/", Code: 307, KeepQuery: true},
	)
	// Domain redirects (sorted), then the site, then the catch-all.
	if len(routes) != 5 {
		t.Fatalf("routes: %s", js(routes))
	}
	want := []string{
		`{"handle":[{"handler":"static_response","headers":{"Location":["https://example.com/landing"]},"status_code":302}],"match":[{"host":["old.example.com"]}],"terminal":true}`,
		`{"handle":[{"handler":"static_response","headers":{"Location":["https://example.com/{http.request.uri.prefixed_query}"]},"status_code":307}],"match":[{"host":["q.example.com"]}],"terminal":true}`,
		`{"handle":[{"handler":"static_response","headers":{"Location":["https://example.com{http.request.uri.path}{http.request.uri.prefixed_query}"]},"status_code":301}],"match":[{"host":["www.example.com"]}],"terminal":true}`,
	}
	for i, w := range want {
		if got := js(routes[i]); got != w {
			t.Errorf("route %d:\n got %s\nwant %s", i, got, w)
		}
	}
	if !strings.Contains(js(routes[3]), `"host":["example.com"]`) {
		t.Fatalf("site route: %s", js(routes[3]))
	}
}

func TestBuildPathRedirectsBeforeFiles(t *testing.T) {
	sites := []Site{
		{Name: "a", Domains: []string{"a.example.com", "www.a.example.com"}, Root: "/r/a", Live: true},
		{Name: "b", Domains: []string{"b.example.com"}, Root: "/r/b"},
	}
	_, routes := redirectRoutesOf(t, sites,
		Redirect{Domain: "a.example.com", Path: "/blog/*", Target: "https://news.example.com/", Code: 308, KeepPath: true, KeepQuery: true},
		Redirect{Domain: "a.example.com", Path: "/old.html", Target: "/new.html", Code: 301},
		Redirect{Domain: "a.example.com", Path: "/blog/x/*", Target: "/x", Code: 302},
		Redirect{Domain: "www.a.example.com", Path: "/*", Target: "https://a.example.com", Code: 301, KeepPath: true},
		Redirect{Domain: "b.example.com", Path: "/go", Target: "https://example.org/?a=1", Code: 307},
	)
	sub := func(i int) []any {
		r := routes[i].(map[string]any)
		h := r["handle"].([]any)[0].(map[string]any)
		return h["routes"].([]any)
	}
	a := sub(0)
	want := []string{
		// a.example.com: exact first, then the longer prefix, then the shorter.
		`{"handle":[{"handler":"static_response","headers":{"Location":["/new.html"]},"status_code":301}],"match":[{"host":["a.example.com"],"path_regexp":{"name":"redirect","pattern":"^/old\\.html$"}}],"terminal":true}`,
		`{"handle":[{"handler":"static_response","headers":{"Location":["/x"]},"status_code":302}],"match":[{"host":["a.example.com"],"path_regexp":{"name":"redirect","pattern":"^/blog/x$"}}],"terminal":true}`,
		`{"handle":[{"handler":"static_response","headers":{"Location":["/x"]},"status_code":302}],"match":[{"host":["a.example.com"],"path_regexp":{"name":"redirect","pattern":"^/blog/x/+(.*)$"}}],"terminal":true}`,
		`{"handle":[{"handler":"static_response","headers":{"Location":["https://news.example.com/{http.request.uri.prefixed_query}"]},"status_code":308}],"match":[{"host":["a.example.com"],"path_regexp":{"name":"redirect","pattern":"^/blog$"}}],"terminal":true}`,
		`{"handle":[{"handler":"static_response","headers":{"Location":["https://news.example.com/{http.regexp.redirect.1}{http.request.uri.prefixed_query}"]},"status_code":308}],"match":[{"host":["a.example.com"],"path_regexp":{"name":"redirect","pattern":"^/blog/+(.*)$"}}],"terminal":true}`,
		`{"handle":[{"handler":"static_response","headers":{"Location":["https://a.example.com/{http.regexp.redirect.1}"]},"status_code":301}],"match":[{"host":["www.a.example.com"],"path_regexp":{"name":"redirect","pattern":"^/+(.*)$"}}],"terminal":true}`,
	}
	if len(a) != len(want)+1 {
		t.Fatalf("site a subroute: %s", js(a))
	}
	for i, w := range want {
		if got := js(a[i]); got != w {
			t.Errorf("a route %d:\n got %s\nwant %s", i, got, w)
		}
	}
	if last := js(a[len(a)-1]); !strings.Contains(last, `"file_server"`) || strings.Contains(last, "match") {
		t.Errorf("files come last: %s", last)
	}
	// Redirects also work before a site has content.
	b := sub(1)
	if len(b) != 2 || !strings.Contains(js(b[0]), `"Location":["https://example.org/?a=1"]`) || !strings.Contains(js(b[1]), `"status_code":503`) {
		t.Errorf("site b: %s", js(b))
	}
}

func TestBuildRedirectsRefusesBadInput(t *testing.T) {
	for _, r := range []Redirect{
		{Domain: "a.example.com", Target: "https://x.example/", Code: 200},
		{Domain: "a.example.com", Target: "https://x.example/{http.request.host}", Code: 301},
		{Domain: "a.example.com", Path: "/{x}", Target: "/", Code: 301},
		{Domain: "a.example.com", Path: "nope", Target: "/", Code: 301},
		{Domain: "a.example.com", Path: "/a*/b", Target: "/", Code: 301},
		{Domain: "", Target: "/", Code: 301},
	} {
		if _, err := Build(Options{TLSMode: TLSOff}, nil, r); err == nil {
			t.Errorf("%+v accepted", r)
		}
	}
	d := Redirect{Domain: "a.example.com", Target: "https://x.example/", Code: 301}
	if _, err := Build(Options{TLSMode: TLSOff}, nil, d, d); err == nil {
		t.Error("two redirects for one domain accepted")
	}
}
