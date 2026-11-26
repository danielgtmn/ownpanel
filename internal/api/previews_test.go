package api

import (
	"net/url"
	"strings"
	"testing"
)

type publication struct {
	CurrentRelease   string `json:"current_release"`
	PublishedRelease string `json:"published_release"`
	PublishedLive    bool   `json:"published_live"`
	PreviewURL       string `json:"preview_url"`
	PreviewAddresses bool   `json:"preview_addresses"`
	Versions         []struct {
		ID         string `json:"id"`
		Live       bool   `json:"live"`
		Pending    bool   `json:"pending"`
		PreviewURL string `json:"preview_url"`
	} `json:"versions"`
}

func TestPublishNotLiveAndPreviewPermission(t *testing.T) {
	e := newEnv(t)
	if rec := e.as("PUT", "/api/sites/blog", `{"domains":["blog.example.com"]}`); rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	rec := e.as("POST", "/api/sites/blog/deployments", tarGz(t, "v1"))
	live := decode[publication](t, rec)
	if rec.Code != 201 || !live.PublishedLive || live.CurrentRelease != live.PublishedRelease {
		t.Fatalf("live publish: %d %s", rec.Code, rec.Body)
	}
	if rec := e.as("POST", "/api/sites/blog/deployments?live=maybe", tarGz(t, "x")); rec.Code != 400 {
		t.Fatalf("bad live: %d %s", rec.Code, rec.Body)
	}

	if rec := e.as("PUT", "/api/settings", `{"preview_domain":"preview.example.net"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"preview_domain":"preview.example.net"`) {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body)
	}
	rec = e.as("POST", "/api/sites/blog/deployments?live=false", tarGz(t, "v2"))
	p := decode[publication](t, rec)
	if rec.Code != 201 || p.PublishedLive || p.CurrentRelease != live.CurrentRelease || p.PublishedRelease == live.CurrentRelease ||
		!p.PreviewAddresses || p.PreviewURL == "" || len(p.Versions) != 2 || !p.Versions[0].Pending || !p.Versions[1].Live {
		t.Fatalf("not live: %d %s", rec.Code, rec.Body)
	}
	u, err := url.Parse(p.PreviewURL)
	if err != nil || !strings.HasSuffix(u.Hostname(), ".preview.example.net") || u.Scheme != "http" || u.Port() != "8080" {
		t.Fatalf("preview address %q", p.PreviewURL)
	}

	// The permission endpoint: only for the web server on this machine.
	ask := "/api/previews/certificate-allowed?domain=" + url.QueryEscape(u.Hostname())
	if rec := e.do(req{method: "GET", path: ask, remote: "127.0.0.1:5000"}); rec.Code != 200 {
		t.Fatalf("known preview: %d %s", rec.Code, rec.Body)
	}
	for name, r := range map[string]req{
		"unknown name":    {method: "GET", path: "/api/previews/certificate-allowed?domain=abc.preview.example.net", remote: "127.0.0.1:5000"},
		"no name":         {method: "GET", path: "/api/previews/certificate-allowed", remote: "[::1]:5000"},
		"from outside":    {method: "GET", path: ask, remote: "203.0.113.9:5000"},
		"through a proxy": {method: "GET", path: ask, remote: "127.0.0.1:5000", headers: map[string]string{"X-Forwarded-For": "203.0.113.9"}},
	} {
		if rec := e.do(r); rec.Code != 404 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}

	// Make it live.
	rec = e.as("POST", "/api/sites/blog/rollback", `{"release":"`+p.PublishedRelease+`"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"current_release":"`+p.PublishedRelease+`"`) {
		t.Fatalf("make live: %d %s", rec.Code, rec.Body)
	}

	// Only admins set the preview domain and point it.
	_, tok := e.createToken("write", `["blog"]`)
	if rec := e.do(req{method: "PUT", path: "/api/settings", body: `{"preview_domain":""}`, token: tok}); rec.Code != 403 {
		t.Fatalf("site token: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(req{method: "POST", path: "/api/settings/preview-domain/point", token: tok}); rec.Code != 403 {
		t.Fatalf("site token point: %d %s", rec.Code, rec.Body)
	}
	// A site token may publish without making live.
	if rec := e.do(req{method: "POST", path: "/api/sites/blog/deployments?live=false", body: tarGz(t, "v3"), token: tok}); rec.Code != 201 {
		t.Fatalf("site token publish: %d %s", rec.Code, rec.Body)
	}
}
