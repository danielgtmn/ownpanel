package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/dns/hetzner/hetznertest"
)

var redirectKeys = []string{"id", "owner", "source_domain", "source_path", "target", "code", "keep_path", "keep_query", "created_at"}

type redirectJSON struct {
	ID           int64  `json:"id"`
	Owner        string `json:"owner"`
	SourceDomain string `json:"source_domain"`
	SourcePath   string `json:"source_path"`
	Target       string `json:"target"`
	Code         int    `json:"code"`
	KeepPath     bool   `json:"keep_path"`
	KeepQuery    bool   `json:"keep_query"`
	CreatedAt    string `json:"created_at"`
}

func TestRedirectRoutes(t *testing.T) {
	d := newDNSEnv(t, "203.0.113.5")
	wantStatus(t, d.root("PUT", "/api/sites/blog", `{"domains":["blog.example.com"]}`), 200, "")

	rec := d.root("GET", "/api/redirects", "")
	if rec.Code != 200 || rec.Body.String() != "[]\n" {
		t.Fatalf("empty list: %d %s", rec.Code, rec.Body)
	}

	rec = d.root("POST", "/api/redirects", `{"source_domain":"WWW.blog.example.com","source_path":"","target":"https://blog.example.com","code":301,"keep_path":true,"keep_query":true}`)
	wantStatus(t, rec, 201, "")
	wantKeys(t, rec.Body.Bytes(), redirectKeys...)
	dom := decode[redirectJSON](t, rec)
	if _, err := time.Parse(time.RFC3339, dom.CreatedAt); err != nil || dom.ID == 0 || dom.Owner != "root" ||
		dom.SourceDomain != "www.blog.example.com" || dom.SourcePath != "" || dom.Target != "https://blog.example.com" ||
		dom.Code != 301 || !dom.KeepPath || !dom.KeepQuery {
		t.Fatalf("created %s", rec.Body)
	}

	rec = d.root("POST", "/api/redirects", `{"source_domain":"blog.example.com","source_path":"/old/*","target":"/new","code":308,"keep_path":true,"keep_query":false}`)
	wantStatus(t, rec, 201, "")
	path := decode[redirectJSON](t, rec)

	rec = d.root("GET", "/api/redirects", "")
	list := decode[[]json.RawMessage](t, rec)
	if rec.Code != 200 || len(list) != 2 {
		t.Fatalf("list: %s", rec.Body)
	}
	wantKeys(t, list[0], redirectKeys...)
	if !strings.Contains(string(list[0]), `"source_path":"/old/*"`) {
		t.Fatalf("sorted by domain: %s", rec.Body)
	}

	rec = d.root("GET", "/api/sites/blog/redirects", "")
	site := decode[[]redirectJSON](t, rec)
	if rec.Code != 200 || len(site) != 1 || site[0].ID != path.ID {
		t.Fatalf("site redirects: %s", rec.Body)
	}
	wantStatus(t, d.root("GET", "/api/sites/nope/redirects", ""), 404, "")

	rec = d.root("PUT", fmt.Sprintf("/api/redirects/%d", path.ID), `{"source_domain":"blog.example.com","source_path":"/older","target":"https://example.org/x","code":302}`)
	wantStatus(t, rec, 200, "")
	wantKeys(t, rec.Body.Bytes(), redirectKeys...)
	if u := decode[redirectJSON](t, rec); u.SourcePath != "/older" || u.Code != 302 || u.KeepPath || u.CreatedAt != path.CreatedAt {
		t.Fatalf("updated %s", rec.Body)
	}

	// Errors.
	wantStatus(t, d.root("POST", "/api/redirects", `{"source_domain":"a.example.com","target":"https://example.com","code":200}`), 400, "The status code must be 301, 302, 307 or 308")
	wantStatus(t, d.root("POST", "/api/redirects", `{"source_domain":"a.example.com","target":"https://a.example.com/","code":301}`), 400, "redirect again forever")
	wantStatus(t, d.root("POST", "/api/redirects", `{"source_domain":"a.example.com","target":"https://example.com","code":"301"}`), 400, "JSON")
	wantStatus(t, d.root("POST", "/api/redirects", `{"source_domain":"www.blog.example.com","target":"https://example.com","code":301}`), 409, "already redirects")
	wantStatus(t, d.root("POST", "/api/redirects", `{"source_domain":"blog.example.com","target":"https://example.com","code":301}`), 400, "belongs to the site blog")
	wantStatus(t, d.root("PUT", "/api/redirects/9999", `{"source_domain":"a.example.com","target":"https://example.com","code":301}`), 404, "")
	wantStatus(t, d.root("PUT", "/api/redirects/abc", `{}`), 404, "")
	wantStatus(t, d.bob("PUT", fmt.Sprintf("/api/redirects/%d", dom.ID), `{"source_domain":"www.blog.example.com","target":"https://example.com","code":301}`), 404, "")
	wantStatus(t, d.bob("POST", "/api/redirects", `{"source_domain":"blog.example.com","source_path":"/x","target":"/y","code":301}`), 400, "not a domain of one of your sites")
	wantStatus(t, d.call(req{method: "GET", path: "/api/redirects"}), 401, "")
	wantStatus(t, d.call(req{method: "POST", path: "/api/redirects", cookie: d.cookie, body: `{}`}), 403, "X-Ownpanel-Request")
	_, readTok := d.createToken("read", "null")
	wantStatus(t, d.call(req{method: "GET", path: "/api/redirects", token: readTok}), 200, "")
	wantStatus(t, d.call(req{method: "DELETE", path: fmt.Sprintf("/api/redirects/%d", dom.ID), token: readTok}), 403, "can only read")
	_, siteTok := d.createToken("write", `["blog"]`)
	rec = d.call(req{method: "GET", path: "/api/redirects", token: siteTok})
	if l := decode[[]redirectJSON](t, rec); len(l) != 1 || l[0].ID != path.ID {
		t.Fatalf("site token sees: %s", rec.Body)
	}
	wantStatus(t, d.call(req{method: "POST", path: "/api/redirects", token: siteTok,
		body: `{"source_domain":"x.example.org","target":"https://example.com","code":301}`}), 403, "domain redirects")

	wantStatus(t, d.root("DELETE", fmt.Sprintf("/api/redirects/%d", path.ID), ""), 204, "")
	wantStatus(t, d.root("DELETE", fmt.Sprintf("/api/redirects/%d", path.ID), ""), 404, "")
}

func TestDomainStatusAndPointRoutes(t *testing.T) {
	d := newDNSEnv(t, "203.0.113.5", "2001:db8::5")
	id := fmt.Sprint(d.connect(d.root, "main", hzToken))
	wantStatus(t, d.root("PUT", "/api/sites/blog", `{"domains":["blog.example.com"]}`), 200, "")
	wantStatus(t, d.root("POST", "/api/redirects", `{"source_domain":"old.example.com","target":"https://blog.example.com","code":301}`), 201, "")

	// The single status is the same object as an element of the site's list.
	rec := d.root("GET", "/api/dns/domain-status?domain=blog.example.com", "")
	want := `{"domain":"blog.example.com","state":"points_elsewhere","found":["198.51.100.1"],` +
		`"zone":{"connection_id":` + id + `,"connection_name":"main","zone":"example.com","record_name":"blog"}}` + "\n"
	if rec.Code != 200 || rec.Body.String() != want {
		t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
	site := d.root("GET", "/api/sites/blog/domains/status", "")
	if site.Body.String() != "["+strings.TrimSuffix(want, "\n")+"]\n" {
		t.Fatalf("site status differs: %s", site.Body)
	}
	rec = d.root("GET", "/api/dns/domain-status?domain=old.example.com", "")
	if rec.Code != 200 || rec.Body.String() != `{"domain":"old.example.com","state":"no_record","found":[],`+
		`"zone":{"connection_id":`+id+`,"connection_name":"main","zone":"example.com","record_name":"old"}}`+"\n" {
		t.Fatalf("redirect status: %d %s", rec.Code, rec.Body)
	}
	wantStatus(t, d.root("GET", "/api/dns/domain-status?domain=unknown.example.com", ""), 404, "no site or redirect")
	wantStatus(t, d.root("GET", "/api/dns/domain-status", ""), 404, "")
	wantStatus(t, d.bob("GET", "/api/dns/domain-status?domain=old.example.com", ""), 404, "")

	rec = d.root("POST", "/api/dns/point", `{"domain":"old.example.com"}`)
	if rec.Code != 200 || rec.Body.String() != `{"changed":[{"name":"old","type":"A","ttl":300,"values":["203.0.113.5"],"locked":false},`+
		`{"name":"old","type":"AAAA","ttl":300,"values":["2001:db8::5"],"locked":false}]}`+"\n" {
		t.Fatalf("point: %d %s", rec.Code, rec.Body)
	}
	wantStatus(t, d.root("POST", "/api/dns/point", `{"domain":"old.example.com"}`), 200, "")
	wantStatus(t, d.root("POST", "/api/dns/point", `nope`), 400, "JSON")
	wantStatus(t, d.bob("POST", "/api/dns/point", `{"domain":"old.example.com"}`), 404, "")
	// The site-scoped route still works.
	wantStatus(t, d.root("POST", "/api/sites/blog/domains/blog.example.com/point", ""), 200, "")
}

func TestDNSZoneRoutes(t *testing.T) {
	d := newDNSEnv(t, "203.0.113.5")
	id := fmt.Sprint(d.connect(d.root, "main", hzToken))
	bobID := fmt.Sprint(d.connect(d.bob, "bobs", hzBobToken))
	zones := "/api/dns/connections/" + id + "/zones"

	rec := d.root("POST", zones, `{"name":"fresh.com"}`)
	if rec.Code != 201 || rec.Body.String() != `{"connection_id":`+id+`,"connection_name":"main","name":"fresh.com","read_only":false,`+
		`"nameservers":["hydrogen.ns.hetzner.com","oxygen.ns.hetzner.com","helium.ns.hetzner.de"],"adopted":true,"used_by":[]}`+"\n" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	wantKeys(t, rec.Body.Bytes(), "name", "connection_id", "connection_name", "read_only", "nameservers", "adopted", "used_by")
	rec = d.root("GET", "/api/dns/zones", "")
	if !strings.Contains(rec.Body.String(), `"name":"fresh.com","read_only":false,"nameservers":["hydrogen.ns.hetzner.com"`) {
		t.Fatalf("zone list: %s", rec.Body)
	}
	wantStatus(t, d.root("POST", zones, `{"name":"fresh.com"}`), 409, "already has the domain")
	wantStatus(t, d.root("POST", zones, `{"name":"www.fresh.com"}`), 400, "not a domain you can register")
	wantStatus(t, d.root("POST", zones, `{}`), 400, "Enter the domain")
	wantStatus(t, d.root("POST", zones, `x`), 400, "JSON")
	d.hz[hzToken].Fail("POST /zones", 1, hetznertest.Error{Status: 409, Code: "uniqueness_error", Message: "exists"})
	wantStatus(t, d.root("POST", zones, `{"name":"taken.com"}`), 409, "already exists at Hetzner")
	wantStatus(t, d.root("POST", "/api/dns/connections/"+bobID+"/zones", `{"name":"x.com"}`), 403, "belongs to bob")
	wantStatus(t, d.bob("POST", zones, `{"name":"x.com"}`), 404, "")
	_, readTok := d.createToken("read", "null")
	wantStatus(t, d.call(req{method: "POST", path: zones, token: readTok, body: `{"name":"x.com"}`}), 403, "")
	_, writeTok := d.createToken("write", "null")
	wantStatus(t, d.call(req{method: "POST", path: zones, token: writeTok, body: `{"name":"tokened.com"}`}), 201, "")

	// Delete.
	wantStatus(t, d.root("DELETE", zones+"/fresh.com", `{"confirm":"fresh.co"}`), 400, "type the domain name fresh.com exactly")
	wantStatus(t, d.root("DELETE", zones+"/fresh.com", ``), 400, "JSON")
	wantStatus(t, d.root("PUT", "/api/sites/blog", `{"domains":["blog.fresh.com"]}`), 200, "")
	wantStatus(t, d.root("POST", "/api/redirects", `{"source_domain":"www.fresh.com","target":"https://blog.fresh.com","code":301}`), 201, "")
	wantStatus(t, d.root("DELETE", zones+"/fresh.com", `{"confirm":"fresh.com"}`), 409, "still in use by the site blog (blog.fresh.com), the redirect of www.fresh.com")
	wantStatus(t, d.root("DELETE", "/api/sites/blog", ""), 204, "")
	rec = d.root("GET", "/api/redirects", "")
	rid := decode[[]redirectJSON](t, rec)[0].ID
	wantStatus(t, d.root("DELETE", fmt.Sprintf("/api/redirects/%d", rid), ""), 204, "")
	d.hz[hzToken].ProtectZone("fresh.com", true)
	wantStatus(t, d.root("DELETE", zones+"/fresh.com", `{"confirm":"fresh.com"}`), 409, "protected against deletion")
	d.hz[hzToken].ProtectZone("fresh.com", false)
	wantStatus(t, d.root("DELETE", zones+"/fresh.com", `{"confirm":"fresh.com"}`), 204, "")
	wantStatus(t, d.root("DELETE", zones+"/fresh.com", `{"confirm":"fresh.com"}`), 404, "not part of")
	if d.hz[hzToken].HasZone("fresh.com") {
		t.Fatal("zone still at Hetzner")
	}
}
