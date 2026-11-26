package panel

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	pngData = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	icoData = "\x00\x00\x01\x00\x01\x00\x10\x10"
	gifData = "GIF89a\x01\x00\x01\x00"
	svgData = `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><rect width="1" height="1"/></svg>`
)

// publishIcon creates the site "blog" with files as its live release.
func publishIcon(t *testing.T, files map[string]string) (*Service, string) {
	t.Helper()
	svc, _, sites := newService(t)
	ctx := context.Background()
	if _, err := svc.SaveSite(ctx, admin, "blog", []string{"blog.example.com"}); err != nil {
		t.Fatal(err)
	}
	site, err := svc.Publish(ctx, admin, "blog", tarGz(t, files))
	if err != nil {
		t.Fatal(err)
	}
	return svc, filepath.Join(sites, "blog", "releases", site.Current)
}

func page(head string) string {
	return "<!doctype html><html><head>" + head + "</head><body><link rel=icon href=body.png></body></html>"
}

func TestSiteIconLookup(t *testing.T) {
	cases := []struct {
		name     string
		files    map[string]string
		path     string
		mimeType string
	}{
		{"link rel icon", map[string]string{"index.html": page(`<link rel="icon" href="img/logo.png?v=2">`), "img/logo.png": pngData, "favicon.svg": svgData},
			"img/logo.png", "image/png"},
		{"shortcut icon with root path", map[string]string{"index.html": page(`<LINK REL="Shortcut Icon" HREF="/static/f.gif">`), "static/f.gif": gifData},
			"static/f.gif", "image/gif"},
		{"icon before apple-touch-icon", map[string]string{"index.html": page(`<link rel="apple-touch-icon" href="touch.png"><link rel="icon" href="i.svg">`), "touch.png": pngData, "i.svg": svgData},
			"i.svg", "image/svg+xml"},
		{"apple-touch-icon link", map[string]string{"index.html": page(`<link rel="apple-touch-icon" href="touch.png">`), "touch.png": pngData},
			"touch.png", "image/png"},
		{"data url", map[string]string{"index.html": page(`<link rel="icon" href="data:image/png;base64,` + base64.StdEncoding.EncodeToString([]byte(pngData)) + `">`), "favicon.svg": svgData},
			"index.html#data", "image/png"},
		{"missing link target falls back", map[string]string{"index.html": page(`<link rel="icon" href="gone.png">`), "favicon.png": pngData},
			"favicon.png", "image/png"},
		{"favicon.svg", map[string]string{"index.html": "x", "favicon.svg": svgData, "favicon.ico": icoData}, "favicon.svg", "image/svg+xml"},
		{"favicon.png", map[string]string{"index.html": "x", "favicon.png": pngData, "favicon.ico": icoData}, "favicon.png", "image/png"},
		{"favicon.ico", map[string]string{"index.html": "x", "favicon.ico": icoData}, "favicon.ico", "image/x-icon"},
		{"png inside favicon.ico", map[string]string{"index.html": "x", "favicon.ico": pngData}, "favicon.ico", "image/png"},
		{"apple-touch-icon.png", map[string]string{"index.html": "x", "apple-touch-icon.png": pngData}, "apple-touch-icon.png", "image/png"},
		{"absolute url ignored", map[string]string{"index.html": page(`<link rel="icon" href="https://tracker.example/i.png"><link rel="icon" href="//cdn.example/i.png">`), "favicon.ico": icoData},
			"favicon.ico", "image/x-icon"},
		{"wrong content ignored", map[string]string{"index.html": page(`<link rel="icon" href="fake.png">`), "fake.png": "<script>alert(1)</script>", "favicon.ico": "not an icon", "favicon.svg": svgData},
			"favicon.svg", "image/svg+xml"},
		{"other file types ignored", map[string]string{"index.html": page(`<link rel="icon" href="icon.html">`), "icon.html": svgData, "apple-touch-icon.png": pngData},
			"apple-touch-icon.png", "image/png"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, dir := publishIcon(t, c.files)
			icon, err := svc.SiteIcon(context.Background(), admin, "blog")
			if err != nil {
				t.Fatal(err)
			}
			if icon.Path != c.path || icon.ContentType != c.mimeType || icon.Release != filepath.Base(dir) {
				t.Fatalf("icon = %q %q %q, want %q %q", icon.Path, icon.ContentType, icon.Release, c.path, c.mimeType)
			}
			if icon.Path != "index.html#data" && string(icon.Data) != c.files[icon.Path] {
				t.Fatalf("data = %q", icon.Data)
			}
		})
	}
}

func TestSiteIconRefused(t *testing.T) {
	big := pngData + strings.Repeat("x", MaxSiteIconBytes)
	bigData := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte(pngData+strings.Repeat("x", 70<<10)))
	cases := []struct {
		name  string
		files map[string]string
	}{
		{"nothing", map[string]string{"index.html": "<html><head><title>x</title></head></html>"}},
		{"oversized file", map[string]string{"index.html": "x", "favicon.png": big}},
		{"oversized linked file", map[string]string{"index.html": page(`<link rel="icon" href="big.png">`), "big.png": big}},
		{"oversized data url", map[string]string{"index.html": page(`<link rel="icon" href="` + bigData + `">`)}},
		{"absolute url only", map[string]string{"index.html": page(`<link rel="icon" href="https://example.org/favicon.ico">`)}},
		{"link in body only", map[string]string{"index.html": page(""), "body.png": pngData}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, _ := publishIcon(t, c.files)
			_, err := svc.SiteIcon(context.Background(), admin, "blog")
			wantKind(t, err, NotFound)
		})
	}
}

func TestSiteIconStaysInsideRelease(t *testing.T) {
	svc, dir := publishIcon(t, map[string]string{
		"index.html": page(`<link rel="icon" href="../outside.png"><link rel="icon" href="img/../../outside.png"><link rel="icon" href="%2e%2e/outside.png"><link rel="icon" href="link.png">`),
	})
	outside := filepath.Join(filepath.Dir(dir), "outside.png")
	if err := os.WriteFile(outside, []byte(pngData), 0o644); err != nil {
		t.Fatal(err)
	}
	// Releases never contain links; one put there by hand must not lead out.
	if err := os.Symlink(outside, filepath.Join(dir, "link.png")); err != nil {
		t.Fatal(err)
	}
	_, err := svc.SiteIcon(context.Background(), admin, "blog")
	wantKind(t, err, NotFound)
}

func TestSiteIconOfUnpublishedOrHiddenSite(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	if _, err := svc.SaveSite(ctx, admin, "draft", []string{"draft.example.com"}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.SiteIcon(ctx, admin, "draft")
	wantKind(t, err, NotFound)
	_, err = svc.SiteIcon(ctx, admin, "missing")
	wantKind(t, err, NotFound)
	_, err = svc.SiteIcon(ctx, Actor{}, "draft")
	wantKind(t, err, Unauthenticated)
	_, err = svc.SiteIcon(ctx, Actor{UserID: "1", Name: "root", Admin: true, Via: ViaToken, ReadOnly: true, Sites: []string{"other"}}, "draft")
	wantKind(t, err, NotFound)
}
