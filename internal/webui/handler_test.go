package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

var app = fstest.MapFS{
	"index.html":         {Data: []byte("<!doctype html><title>app</title>")},
	"assets/app-abc.js":  {Data: []byte("console.log(1)")},
	"favicon.svg":        {Data: []byte("<svg/>")},
	"assets/nested/x.js": {Data: []byte("x")},
}

func get(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestAssetIsServedWithLongCache(t *testing.T) {
	rec := get(Handler(app), "GET", "/assets/app-abc.js")
	if rec.Code != 200 || rec.Body.String() != "console.log(1)" {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control %q", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("Content-Type %q", ct)
	}
}

func TestIndexIsNotCached(t *testing.T) {
	for _, p := range []string{"/", "/index.html"} {
		rec := get(Handler(app), "GET", p)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>app</title>") {
			t.Fatalf("%s: %d %q", p, rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("%s: Cache-Control %q", p, got)
		}
	}
}

func TestDeepLinkFallsBackToIndex(t *testing.T) {
	for _, p := range []string{"/sites/blog", "/sites/blog/", "/assets/missing.js", "/assets"} {
		rec := get(Handler(app), "GET", p)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>app</title>") {
			t.Fatalf("%s: %d %q", p, rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("%s: Cache-Control %q", p, got)
		}
	}
	if rec := get(Handler(app), "HEAD", "/sites/blog"); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %q", rec.Code, rec.Body)
	}
}

func TestAPIPathsAreNotAnsweredByFallback(t *testing.T) {
	for _, p := range []string{"/api", "/api/", "/api/whatever", "/api/sites/x"} {
		if rec := get(Handler(app), "GET", p); rec.Code != 404 || strings.Contains(rec.Body.String(), "<title>") {
			t.Fatalf("%s: %d %q", p, rec.Code, rec.Body)
		}
	}
}

func TestOnlyGetAndHead(t *testing.T) {
	for _, m := range []string{"POST", "PUT", "DELETE"} {
		if rec := get(Handler(app), m, "/"); rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: %d", m, rec.Code)
		}
	}
}

func TestNoDirectoryListing(t *testing.T) {
	rec := get(Handler(app), "GET", "/assets/nested/")
	if strings.Contains(rec.Body.String(), "x.js") {
		t.Fatalf("directory listing: %q", rec.Body)
	}
}

func TestSecurityHeaders(t *testing.T) {
	for _, p := range []string{"/", "/assets/app-abc.js", "/deep/link"} {
		h := get(Handler(app), "GET", p).Header()
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" || h.Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("%s: %v", p, h)
		}
		if got := h.Get("Content-Security-Policy"); got != ContentSecurityPolicy {
			t.Fatalf("%s: Content-Security-Policy %q", p, got)
		}
	}
	if got := get(Handler(nil), "GET", "/").Header().Get("Content-Security-Policy"); got != ContentSecurityPolicy {
		t.Fatalf("without app: Content-Security-Policy %q", got)
	}
}

func TestContentSecurityPolicyIsStrict(t *testing.T) {
	directives := map[string]string{}
	for _, d := range strings.Split(ContentSecurityPolicy, ";") {
		name, value, _ := strings.Cut(strings.TrimSpace(d), " ")
		if _, dup := directives[name]; dup {
			t.Fatalf("directive %q twice", name)
		}
		directives[name] = value
	}
	want := map[string]string{
		"default-src":     "'self'",
		"script-src":      "'self'",
		"style-src":       "'self'",
		"img-src":         "'self' data:",
		"connect-src":     "'self'",
		"object-src":      "'none'",
		"base-uri":        "'none'",
		"form-action":     "'self'",
		"frame-ancestors": "'none'",
	}
	for name, value := range want {
		if directives[name] != value {
			t.Errorf("%s: got %q, want %q", name, directives[name], value)
		}
	}
	for _, bad := range []string{"'unsafe-inline'", "'unsafe-eval'", "*", "http:", "https:"} {
		for name, value := range directives {
			for _, v := range strings.Fields(value) {
				if v == bad {
					t.Errorf("%s allows %s", name, bad)
				}
			}
		}
	}
}

func TestNoEmbeddedApp(t *testing.T) {
	rec := get(Handler(nil), "GET", "/")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "not included") || !strings.Contains(body, "/api") {
		t.Fatalf("%d %q", rec.Code, body)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("Content-Type %q", rec.Header().Get("Content-Type"))
	}
	if rec := get(Handler(nil), "GET", "/sites/x"); rec.Code != 404 {
		t.Fatalf("deep link without app: %d", rec.Code)
	}
}
