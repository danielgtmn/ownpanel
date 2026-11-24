package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

func tarFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.String()
}

func TestSiteIconRoute(t *testing.T) {
	e := newEnv(t)
	const svg = `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`
	for _, step := range []struct{ method, path, body string }{
		{"PUT", "/api/sites/blog", `{"domains":["blog.example.com"]}`},
		{"POST", "/api/sites/blog/deployments", tarFiles(t, map[string]string{
			"index.html":  `<html><head><link rel="icon" href="/favicon.svg"></head></html>`,
			"favicon.svg": svg,
		})},
		{"PUT", "/api/sites/plain", `{"domains":["plain.example.com"]}`},
		{"POST", "/api/sites/plain/deployments", tarGz(t, "no icon")},
		{"PUT", "/api/sites/draft", `{"domains":["draft.example.com"]}`},
	} {
		if rec := e.as(step.method, step.path, step.body); rec.Code >= 300 {
			t.Fatalf("%s %s: %d %s", step.method, step.path, rec.Code, rec.Body)
		}
	}

	rec := e.do(req{method: "GET", path: "/api/sites/blog/icon", cookie: e.cookie})
	if rec.Code != 200 || rec.Body.String() != svg {
		t.Fatalf("icon: %d %q", rec.Code, rec.Body)
	}
	want := map[string]string{
		"Content-Type":            "image/svg+xml",
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'",
		"X-Content-Type-Options":  "nosniff",
		"Cache-Control":           "private, max-age=300",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Fatalf("%s = %q, want %q", k, got, v)
		}
	}
	etag := rec.Header().Get("ETag")
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `/favicon.svg"`) {
		t.Fatalf("ETag = %q", etag)
	}

	again := e.do(req{method: "GET", path: "/api/sites/blog/icon", cookie: e.cookie, headers: map[string]string{"If-None-Match": etag}})
	if again.Code != 304 || again.Body.Len() != 0 {
		t.Fatalf("revalidation: %d %q", again.Code, again.Body)
	}

	for _, name := range []string{"plain", "draft", "missing"} {
		rec := e.do(req{method: "GET", path: "/api/sites/" + name + "/icon", cookie: e.cookie})
		if rec.Code != 404 || rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Header().Get("Content-Type"))
		}
	}

	if rec := e.do(req{method: "GET", path: "/api/sites/blog/icon"}); rec.Code != 401 {
		t.Fatalf("signed out: %d", rec.Code)
	}
	_, limited := e.createToken("read", `["blog"]`)
	if rec := e.do(req{method: "GET", path: "/api/sites/blog/icon", token: limited}); rec.Code != 200 {
		t.Fatalf("read-only token limited to the site: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(req{method: "GET", path: "/api/sites/plain/icon", token: limited}); rec.Code != 404 {
		t.Fatalf("other site with limited token: %d", rec.Code)
	}
}
