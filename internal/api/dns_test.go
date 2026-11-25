package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/deploy"
	"github.com/danielgtmn/ownpanel/internal/dns"
	"github.com/danielgtmn/ownpanel/internal/dns/hetzner"
	"github.com/danielgtmn/ownpanel/internal/dns/hetzner/hetznertest"
	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/ownpanel/internal/store"
	"github.com/danielgtmn/panzerschrank"
)

const (
	hzToken    = "hz-api-token-0123456789abcdef"
	hzBobToken = "hz-api-bob-0123456789abcdef"
)

type staticResolver map[string][]netip.Addr

func (r staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if a, ok := r[host]; ok {
		return a, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// dnsEnv is an env whose panel talks to fake Hetzner projects: hzToken
// holds example.com (root's), hzBobToken holds bob.example (bob's). Every
// response body is kept so tests can check that no token leaks.
type dnsEnv struct {
	*env
	hz        map[string]*hetznertest.Server
	bobCookie string
	bodies    []string
}

func newDNSEnv(t *testing.T, serverIPs ...string) *dnsEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ownpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := panzerschrank.OpenKeyFile(filepath.Join(dir, panzerschrank.KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	ips, err := panel.ParseServerIPs(serverIPs, true)
	if err != nil {
		t.Fatal(err)
	}
	d := &dnsEnv{hz: map[string]*hetznertest.Server{}}
	reject := hetznertest.New(t, "never-accepted")
	for token, zone := range map[string]string{hzToken: "example.com", hzBobToken: "bob.example"} {
		srv := hetznertest.New(t, token)
		srv.AddZone(zone, "primary")
		d.hz[token] = srv
	}
	svc, err := panel.New(panel.Config{
		Store:         st,
		Deploys:       &deploy.Manager{Base: filepath.Join(dir, "sites")},
		Web:           nopWeb{},
		Caddy:         caddy.Options{AdminListen: "127.0.0.1:2019", TLSMode: caddy.TLSOff, HTTPPort: 8080, HTTPSPort: 8443},
		SetupCodeFile: filepath.Join(dir, "setup-code"),
		Secrets:       box,
		ServerIPs:     ips,
		NewDNSProvider: func(provider, token string) (dns.Provider, error) {
			srv := d.hz[token]
			if srv == nil {
				srv = reject
			}
			return hetzner.New(token, hetzner.WithBaseURL(srv.BaseURL()),
				hetzner.WithPollIntervals(time.Millisecond, 2*time.Millisecond), hetzner.WithRetryDelay(time.Millisecond)), nil
		},
		Resolver: staticResolver{"blog.example.com": {netip.MustParseAddr("198.51.100.1")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureSetupCode(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.env = &env{t: t, h: (&Server{Panel: svc}).Handler(), svc: svc, code: filepath.Join(dir, "setup-code")}
	d.cookie = d.setup("root", "root-password-1")
	if rec := d.as("POST", "/api/users", `{"name":"bob","password":"bob-password-1","role":"member"}`); rec.Code != 201 {
		t.Fatalf("create bob: %d %s", rec.Code, rec.Body)
	}
	rec := d.do(req{method: "POST", path: "/api/login", body: `{"name":"bob","password":"bob-password-1"}`})
	if rec.Code != 200 {
		t.Fatalf("login bob: %d %s", rec.Code, rec.Body)
	}
	d.bobCookie = sessionCookie(t, rec)
	t.Cleanup(d.checkNoTokenLeaked)
	return d
}

// call sends a request and keeps the response body.
func (d *dnsEnv) call(r req) *httptest.ResponseRecorder {
	d.t.Helper()
	rec := d.do(r)
	d.bodies = append(d.bodies, rec.Body.String())
	return rec
}

func (d *dnsEnv) root(method, path, body string) *httptest.ResponseRecorder {
	d.t.Helper()
	return d.call(req{method: method, path: path, body: body, cookie: d.cookie, csrf: true})
}

func (d *dnsEnv) bob(method, path, body string) *httptest.ResponseRecorder {
	d.t.Helper()
	return d.call(req{method: method, path: path, body: body, cookie: d.bobCookie, csrf: true})
}

func (d *dnsEnv) checkNoTokenLeaked() {
	rec := d.root("GET", "/api/activity?limit=500", "")
	if rec.Code != 200 {
		d.t.Errorf("activity: %d", rec.Code)
	}
	for _, b := range d.bodies {
		for _, tok := range []string{hzToken, hzBobToken, "hz-wrong-0123456789"} {
			if strings.Contains(b, tok) {
				d.t.Errorf("a response contains a provider token: %s", b)
			}
		}
	}
}

func (d *dnsEnv) connect(as func(string, string, string) *httptest.ResponseRecorder, name, token string) int64 {
	d.t.Helper()
	rec := as("POST", "/api/dns/connections", `{"provider":"hetzner","name":"`+name+`","token":"`+token+`"}`)
	if rec.Code != 201 {
		d.t.Fatalf("connect: %d %s", rec.Code, rec.Body)
	}
	return decode[struct {
		ID int64 `json:"id"`
	}](d.t, rec).ID
}

func keysOf(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func wantKeys(t *testing.T, raw json.RawMessage, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := keysOf(t, raw); !reflect.DeepEqual(got, want) {
		t.Fatalf("keys %v, want %v in %s", got, want, raw)
	}
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, status int, msg string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body)
	}
	if msg != "" {
		e := decode[struct {
			Error string `json:"error"`
		}](t, rec)
		if !strings.Contains(e.Error, msg) {
			t.Fatalf("error %q does not contain %q", e.Error, msg)
		}
	}
}

func TestDNSRoutesJSONShapes(t *testing.T) {
	d := newDNSEnv(t, "203.0.113.5", "2001:db8::5")

	rec := d.root("GET", "/api/server", "")
	if rec.Code != 200 || rec.Body.String() != `{"ips":["203.0.113.5","2001:db8::5"]}`+"\n" {
		t.Fatalf("server: %d %s", rec.Code, rec.Body)
	}

	rec = d.root("POST", "/api/dns/connections", `{"provider":"hetzner","name":"main","token":"hz-wrong-0123456789"}`)
	wantStatus(t, rec, 400, "The token was rejected by Hetzner")

	rec = d.root("POST", "/api/dns/connections", `{"provider":"hetzner","name":"main","token":"`+hzToken+`"}`)
	wantStatus(t, rec, 201, "")
	conn := json.RawMessage(rec.Body.Bytes())
	wantKeys(t, conn, "id", "provider", "name", "owner", "created_at", "last_checked_at", "last_error", "can_write", "zone_count")
	var c struct {
		ID            int64   `json:"id"`
		Provider      string  `json:"provider"`
		Name          string  `json:"name"`
		Owner         string  `json:"owner"`
		CreatedAt     string  `json:"created_at"`
		LastCheckedAt *string `json:"last_checked_at"`
		LastError     string  `json:"last_error"`
		CanWrite      *bool   `json:"can_write"`
		ZoneCount     *int    `json:"zone_count"`
	}
	json.Unmarshal(conn, &c)
	if _, err := time.Parse(time.RFC3339, c.CreatedAt); err != nil || c.Provider != "hetzner" || c.Name != "main" ||
		c.Owner != "root" || c.LastCheckedAt == nil || c.CanWrite != nil || c.ZoneCount == nil || *c.ZoneCount != 1 {
		t.Fatalf("connection %s", conn)
	}
	if !strings.Contains(string(conn), `"can_write":null`) {
		t.Fatalf("can_write must be null while unknown: %s", conn)
	}
	id := fmt.Sprint(c.ID)

	rec = d.root("GET", "/api/dns/connections", "")
	list := decode[[]json.RawMessage](t, rec)
	if rec.Code != 200 || len(list) != 1 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	wantKeys(t, list[0], "id", "provider", "name", "owner", "created_at", "last_checked_at", "last_error", "can_write", "zone_count")

	rec = d.root("POST", "/api/dns/connections/"+id+"/check", "")
	wantStatus(t, rec, 200, "")
	wantKeys(t, rec.Body.Bytes(), "id", "provider", "name", "owner", "created_at", "last_checked_at", "last_error", "can_write", "zone_count")

	rec = d.root("GET", "/api/dns/zones", "")
	if rec.Code != 200 || rec.Body.String() != `{"zones":[{"connection_id":`+id+`,"connection_name":"main","name":"example.com","read_only":false,`+
		`"nameservers":["hydrogen.ns.hetzner.com","oxygen.ns.hetzner.com","helium.ns.hetzner.de"]}],"problems":[]}`+"\n" {
		t.Fatalf("zones: %d %s", rec.Code, rec.Body)
	}

	base := "/api/dns/connections/" + id + "/zones/example.com/records"
	rec = d.root("PUT", base+"/www/A", `{"ttl":null,"values":["198.51.100.7"]}`)
	if rec.Code != 200 || rec.Body.String() != `{"name":"www","type":"A","ttl":null,"values":["198.51.100.7"],"locked":false}`+"\n" {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	rec = d.root("PUT", base+"/@/TXT", `{"ttl":3600,"values":["v=spf1 -all"]}`)
	if rec.Code != 200 || rec.Body.String() != `{"name":"@","type":"TXT","ttl":3600,"values":["v=spf1 -all"],"locked":false}`+"\n" {
		t.Fatalf("put TXT: %d %s", rec.Code, rec.Body)
	}
	wantStatus(t, d.root("PUT", base+"/www/A", `{"ttl":10,"values":["198.51.100.7"]}`), 400, "TTL")
	wantStatus(t, d.root("PUT", base+"/@/NS", `{"values":["ns.example.net"]}`), 400, "NS records of the zone itself")
	wantStatus(t, d.root("PUT", base+"/www/A", `not json`), 400, "JSON")

	rec = d.root("GET", base, "")
	if rec.Code != 200 || rec.Body.String() != `[{"name":"@","type":"TXT","ttl":3600,"values":["v=spf1 -all"],"locked":false},`+
		`{"name":"www","type":"A","ttl":null,"values":["198.51.100.7"],"locked":false}]`+"\n" {
		t.Fatalf("records: %d %s", rec.Code, rec.Body)
	}
	wantStatus(t, d.root("DELETE", base+"/www/A", ""), 204, "")
	wantStatus(t, d.root("DELETE", base+"/@/SOA", ""), 400, "SOA")

	// Per-site status and "point here".
	wantStatus(t, d.root("PUT", "/api/sites/blog", `{"domains":["blog.example.com","shop.example.org"]}`), 200, "")
	rec = d.root("GET", "/api/sites/blog/domains/status", "")
	want := `[{"domain":"blog.example.com","state":"points_elsewhere","found":["198.51.100.1"],` +
		`"zone":{"connection_id":` + id + `,"connection_name":"main","zone":"example.com","record_name":"blog"}},` +
		`{"domain":"shop.example.org","state":"no_record","found":[],"zone":null}]` + "\n"
	if rec.Code != 200 || rec.Body.String() != want {
		t.Fatalf("status: %d\n got %s\nwant %s", rec.Code, rec.Body, want)
	}
	rec = d.root("POST", "/api/sites/blog/domains/blog.example.com/point", "")
	if rec.Code != 200 || rec.Body.String() != `{"changed":[{"name":"blog","type":"A","ttl":300,"values":["203.0.113.5"],"locked":false},`+
		`{"name":"blog","type":"AAAA","ttl":300,"values":["2001:db8::5"],"locked":false}]}`+"\n" {
		t.Fatalf("point: %d %s", rec.Code, rec.Body)
	}
	rec = d.root("POST", "/api/sites/blog/domains/blog.example.com/point", "")
	if rec.Code != 200 || rec.Body.String() != `{"changed":[]}`+"\n" {
		t.Fatalf("point again: %d %s", rec.Code, rec.Body)
	}
	wantStatus(t, d.root("POST", "/api/sites/blog/domains/shop.example.org/point", ""), 409, "one of your DNS connections")
	wantStatus(t, d.root("POST", "/api/sites/blog/domains/other.example.com/point", ""), 404, "")
	d.hz[hzToken].AddRRSet("example.com", hetznertest.RRSet{Name: "cname", Type: "CNAME", Records: []string{"x.example.net."}})
	wantStatus(t, d.root("PUT", "/api/sites/blog", `{"domains":["blog.example.com","cname.example.com"]}`), 200, "")
	wantStatus(t, d.root("POST", "/api/sites/blog/domains/cname.example.com/point", ""), 409, "Cname.example.com has a CNAME record; remove it first")

	wantStatus(t, d.root("DELETE", "/api/dns/connections/"+id, ""), 204, "")
	wantStatus(t, d.root("DELETE", "/api/dns/connections/"+id, ""), 404, "")
	wantStatus(t, d.root("DELETE", "/api/dns/connections/abc", ""), 404, "")
}

func TestDNSRoutesStatusMapping(t *testing.T) {
	d := newDNSEnv(t, "203.0.113.5")
	rootID := fmt.Sprint(d.connect(d.root, "main", hzToken))
	bobID := fmt.Sprint(d.connect(d.bob, "bobs", hzBobToken))
	records := func(id, zone string) string { return "/api/dns/connections/" + id + "/zones/" + zone + "/records" }

	// Provider failures.
	srv := d.hz[hzToken]
	srv.Fail("GET /zones/example.com/rrsets", 1, hetznertest.Error{Status: 503, Code: "unavailable", Message: "maintenance"})
	wantStatus(t, d.root("GET", records(rootID, "example.com"), ""), 502, "Hetzner could not be reached")
	srv.Fail("GET /zones/example.com/rrsets", 1, hetznertest.Error{Status: 429, Code: "rate_limit_exceeded", Message: "limit"})
	wantStatus(t, d.root("GET", records(rootID, "example.com"), ""), 429, "request limit")
	srv.ReadOnlyToken = true
	wantStatus(t, d.root("PUT", records(rootID, "example.com")+"/www/A", `{"values":["198.51.100.7"]}`), 409, "may only read")
	rec := d.root("GET", "/api/dns/connections", "")
	if !strings.Contains(rec.Body.String(), `"can_write":false`) {
		t.Fatalf("read-only token not flagged: %s", rec.Body)
	}
	srv.ReadOnlyToken = false
	wantStatus(t, d.root("GET", records(rootID, "nope.example"), ""), 404, "not part of")

	// Whose connection.
	wantStatus(t, d.bob("GET", records(rootID, "example.com"), ""), 404, "")
	wantStatus(t, d.root("GET", records(bobID, "bob.example"), ""), 403, "belongs to bob")
	rec = d.root("GET", "/api/dns/connections", "")
	if list := decode[[]struct{ Owner string }](t, rec); len(list) != 2 {
		t.Fatalf("admins see every connection: %s", rec.Body)
	}
	rec = d.bob("GET", "/api/dns/connections", "")
	if list := decode[[]struct{ Owner string }](t, rec); len(list) != 1 || list[0].Owner != "bob" {
		t.Fatalf("members see their own: %s", rec.Body)
	}
	rec = d.bob("GET", "/api/dns/zones", "")
	if z := decode[panel.DNSZones](t, rec); len(z.Zones) != 1 || z.Zones[0].Name != "bob.example" {
		t.Fatalf("bob's zones: %s", rec.Body)
	}

	// Access tokens.
	_, readTok := d.createToken("read", "null")
	wantStatus(t, d.call(req{method: "GET", path: "/api/dns/zones", token: readTok}), 200, "")
	wantStatus(t, d.call(req{method: "POST", path: "/api/dns/connections", token: readTok,
		body: `{"provider":"hetzner","name":"x","token":"` + hzToken + `"}`}), 403, "")
	wantStatus(t, d.call(req{method: "PUT", path: records(rootID, "example.com") + "/www/A", token: readTok,
		body: `{"values":["198.51.100.7"]}`}), 403, "")
	wantStatus(t, d.root("PUT", "/api/sites/blog", `{"domains":["blog.example.com"]}`), 200, "")
	_, siteTok := d.createToken("write", `["blog"]`)
	wantStatus(t, d.call(req{method: "GET", path: "/api/dns/zones", token: siteTok}), 403, "limited to certain sites")
	wantStatus(t, d.call(req{method: "GET", path: "/api/sites/blog/domains/status", token: siteTok}), 200, "")
	wantStatus(t, d.call(req{method: "POST", path: "/api/sites/blog/domains/blog.example.com/point", token: siteTok}), 200, "")
	wantStatus(t, d.call(req{method: "GET", path: "/api/server"}), 401, "")

	// Cookie requests that change something need the CSRF header.
	wantStatus(t, d.call(req{method: "POST", path: "/api/dns/connections/" + rootID + "/check", cookie: d.cookie}), 403, "X-Ownpanel-Request")
}

func TestDNSRoutesWithoutServerIP(t *testing.T) {
	d := newDNSEnv(t)
	rec := d.root("GET", "/api/server", "")
	if rec.Code != 200 || rec.Body.String() != `{"ips":[]}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	d.connect(d.root, "main", hzToken)
	wantStatus(t, d.root("PUT", "/api/sites/blog", `{"domains":["blog.example.com"]}`), 200, "")
	msg := "The server's public address is not configured (an admin can enter it in the panel settings)"
	wantStatus(t, d.root("GET", "/api/sites/blog/domains/status", ""), 409, msg)
	wantStatus(t, d.root("POST", "/api/sites/blog/domains/blog.example.com/point", ""), 409, msg)
}
