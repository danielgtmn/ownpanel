package dav

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
	"github.com/danielgtmn/ownpanel/internal/mail/server"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
	"github.com/danielgtmn/ownpanel/internal/mail/web"
)

var _ Backend = (*server.Server)(nil)

const (
	host     = "mail.example.net"
	alice    = "alice@example.org"
	alicePW  = "alice-password-1"
	bob      = "bob@example.org"
	bobPW    = "bob-password-1"
	carol    = "carol@example.de"
	carolPW  = "carol-password-1"
	dave     = "dave@other.test"
	davePW   = "dave-password-1"
	aliasAll = "team@example.org"
)

type env struct {
	t    *testing.T
	dir  string
	srv  *server.Server
	h    *Handler
	ts   *httptest.Server
	cfg  *config.Config
	app  string // an app password of alice
	ip   string
	log  *slog.Logger
	quit func()
}

func baseConfig(t *testing.T) (*config.Config, string) {
	key := config.NewAppPasswordKey()
	id, secret, token := config.NewAppPassword()
	ap := config.AppPassword{ID: id, Name: "Phone", MAC: config.AppPasswordMAC(key, mailtest.ID('a'), id, secret), Created: time.Now().UTC().Truncate(time.Second)}
	return &config.Config{
		Hostname:       host,
		Certificates:   []config.Certificate{mailtest.Cert(t, host, false)},
		AppPasswordKey: key,
		Domains: []config.Domain{
			{Name: "example.org", Organisation: "org-acme"},
			{Name: "example.de", Organisation: "org-acme"},
			{Name: "other.test"},
		},
		Mailboxes: []config.Mailbox{
			{StorageID: mailtest.ID('a'), Address: alice, PasswordHash: mailtest.CheapHash(alicePW), QuotaBytes: 1 << 30, DisplayName: "Alice Example", AppPasswords: []config.AppPassword{ap}},
			{StorageID: mailtest.ID('b'), Address: bob, PasswordHash: mailtest.CheapHash(bobPW)},
			{StorageID: mailtest.ID('c'), Address: carol, PasswordHash: mailtest.CheapHash(carolPW), DisplayName: "Carol"},
			{StorageID: mailtest.ID('d'), Address: dave, PasswordHash: mailtest.CheapHash(davePW)},
		},
		Aliases: []config.Alias{
			{Address: "info@example.org", Targets: []string{alice}},
			{Address: aliasAll, Targets: []string{alice, bob}},
			{Address: "*@example.org", Targets: []string{alice}},
		},
	}, token
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, dir: t.TempDir(), ip: "192.0.2.10"}
	e.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	e.cfg, e.app = baseConfig(t)
	e.start()
	return e
}

func (e *env) start() {
	t := e.t
	srv, err := server.New(server.Options{DataDir: e.dir, Logger: e.log, FreeBytes: func() (int64, error) { return 10 << 30, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Load(context.Background(), e.cfg); err != nil {
		t.Fatal(err)
	}
	e.srv = srv
	e.h = New(Options{Backend: srv, Logger: e.log})
	e.ts = httptest.NewServer(web.Handler(web.Options{DAV: e.h}))
	e.quit = func() {
		e.ts.Close()
		srv.Store().Close()
	}
	t.Cleanup(func() {
		if e.quit != nil {
			e.quit()
		}
	})
}

func (e *env) restart() {
	e.quit()
	e.quit = nil
	e.start()
}

type resp struct {
	code int
	hdr  http.Header
	body string
}

// do sends a request as user (password pw; "" for none).
func (e *env) do(method, path, user, pw string, hdr map[string]string, body string) resp {
	e.t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Host = host
	req.Header.Set("X-Forwarded-For", e.ip)
	if user != "" {
		req.SetBasicAuth(user, pw)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, res.Header, string(b)}
}

func (e *env) as(method, path string, hdr map[string]string, body string) resp {
	e.t.Helper()
	return e.do(method, path, alice, alicePW, hdr, body)
}

func expect(t *testing.T, r resp, code int, contains ...string) {
	t.Helper()
	if r.code != code {
		t.Fatalf("status %d, want %d: %s", r.code, code, r.body)
	}
	for _, c := range contains {
		if !strings.Contains(r.body, c) {
			t.Fatalf("body lacks %q:\n%s", c, r.body)
		}
	}
}

// ---- multistatus parsing ----

type msProp struct {
	Inner string `xml:",innerxml"`
}

type msPropstat struct {
	Prop struct {
		Any []struct {
			XMLName xml.Name
			Inner   string `xml:",innerxml"`
		} `xml:",any"`
	} `xml:"DAV: prop"`
	Status string `xml:"DAV: status"`
}

type msResponse struct {
	Href     string       `xml:"DAV: href"`
	Status   string       `xml:"DAV: status"`
	Propstat []msPropstat `xml:"DAV: propstat"`
}

type multistatus struct {
	Responses []msResponse `xml:"DAV: response"`
	SyncToken string       `xml:"DAV: sync-token"`
}

func parseMS(t *testing.T, r resp) multistatus {
	t.Helper()
	if r.code != http.StatusMultiStatus {
		t.Fatalf("status %d, want 207: %s", r.code, r.body)
	}
	var ms multistatus
	if err := xml.Unmarshal([]byte(r.body), &ms); err != nil {
		t.Fatalf("bad multistatus: %v\n%s", err, r.body)
	}
	return ms
}

func (ms multistatus) get(hrefPath string) *msResponse {
	for i := range ms.Responses {
		if ms.Responses[i].Href == hrefPath {
			return &ms.Responses[i]
		}
	}
	return nil
}

// prop returns the inner XML of a property found with status 200.
func (r *msResponse) prop(local string) (string, bool) {
	if r == nil {
		return "", false
	}
	for _, ps := range r.Propstat {
		if !strings.Contains(ps.Status, " 200 ") {
			continue
		}
		for _, p := range ps.Prop.Any {
			if p.XMLName.Local == local {
				return p.Inner, true
			}
		}
	}
	return "", false
}

func mustProp(t *testing.T, r *msResponse, local string) string {
	t.Helper()
	v, ok := r.prop(local)
	if !ok {
		t.Fatalf("no property %s in %+v", local, r)
	}
	return v
}

var hrefRe = regexp.MustCompile(`<d:href>([^<]*)</d:href>`)

func hrefs(inner string) []string {
	var out []string
	for _, m := range hrefRe.FindAllStringSubmatch(inner, -1) {
		out = append(out, m[1])
	}
	return out
}

const propfindDiscovery = `<?xml version="1.0"?>
<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:card="urn:ietf:params:xml:ns:carddav" xmlns:cs="http://calendarserver.org/ns/">
<d:prop><d:current-user-principal/><d:principal-URL/><d:displayname/><c:calendar-home-set/><card:addressbook-home-set/><c:calendar-user-address-set/><cs:email-address-set/><d:resourcetype/></d:prop></d:propfind>`

const propfindCollections = `<?xml version="1.0"?>
<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:cs="http://calendarserver.org/ns/" xmlns:a="http://apple.com/ns/ical/">
<d:prop><d:resourcetype/><d:displayname/><cs:getctag/><d:sync-token/><d:current-user-privilege-set/><c:supported-calendar-component-set/><a:calendar-color/><d:quota-used-bytes/><d:quota-available-bytes/><d:owner/></d:prop></d:propfind>`

// TestDiscovery follows the chain Apple's programs and DAVx5 use: the
// well-known address, the current principal, its homes, the collections.
func TestDiscovery(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/.well-known/caldav", "/.well-known/carddav"} {
		r := e.do("PROPFIND", p, "", "", map[string]string{"Depth": "0"}, "")
		if r.code != http.StatusMovedPermanently || r.hdr.Get("Location") != "/dav/" {
			t.Fatalf("%s: %d %q", p, r.code, r.hdr.Get("Location"))
		}
		if r := e.do("GET", p, "", "", nil, ""); r.code != http.StatusMovedPermanently {
			t.Fatalf("GET %s: %d", p, r.code)
		}
	}
	// Without credentials: a challenge.
	r := e.do("PROPFIND", "/dav/", "", "", map[string]string{"Depth": "0"}, propfindDiscovery)
	if r.code != http.StatusUnauthorized || !strings.HasPrefix(r.hdr.Get("WWW-Authenticate"), "Basic ") {
		t.Fatalf("no challenge: %d %v", r.code, r.hdr)
	}
	ms := parseMS(t, e.as("PROPFIND", "/dav/", map[string]string{"Depth": "0"}, propfindDiscovery))
	root := ms.get("/dav/")
	principal := hrefs(mustProp(t, root, "current-user-principal"))
	if len(principal) != 1 || principal[0] != "/dav/principals/alice@example.org/" {
		t.Fatalf("principal %v", principal)
	}
	ms = parseMS(t, e.as("PROPFIND", principal[0], map[string]string{"Depth": "0"}, propfindDiscovery))
	p := ms.get(principal[0])
	if v := mustProp(t, p, "displayname"); v != "Alice Example" {
		t.Fatalf("displayname %q", v)
	}
	calHome := hrefs(mustProp(t, p, "calendar-home-set"))
	cardHome := hrefs(mustProp(t, p, "addressbook-home-set"))
	if len(calHome) != 1 || calHome[0] != "/dav/calendars/alice@example.org/" || len(cardHome) != 1 || cardHome[0] != "/dav/addressbooks/alice@example.org/" {
		t.Fatalf("homes %v %v", calHome, cardHome)
	}
	addrs := strings.Join(hrefs(mustProp(t, p, "calendar-user-address-set")), " ")
	for _, want := range []string{"mailto:alice@example.org", "mailto:info@example.org", "mailto:team@example.org"} {
		if !strings.Contains(addrs, want) {
			t.Fatalf("calendar-user-address-set %s lacks %s", addrs, want)
		}
	}
	if strings.Contains(addrs, "*@") {
		t.Fatalf("catch-all in %s", addrs)
	}

	ms = parseMS(t, e.as("PROPFIND", calHome[0], map[string]string{"Depth": "1"}, propfindCollections))
	cal := ms.get("/dav/calendars/alice@example.org/calendar/")
	if rt := mustProp(t, cal, "resourcetype"); !strings.Contains(rt, "calendar") {
		t.Fatalf("resourcetype %s", rt)
	}
	if v := mustProp(t, cal, "displayname"); v != "Calendar" {
		t.Fatalf("displayname %q", v)
	}
	mustProp(t, cal, "getctag")
	if tok := mustProp(t, cal, "sync-token"); !strings.HasPrefix(tok, "urn:ownpanel:sync:") {
		t.Fatalf("sync-token %q", tok)
	}
	if privs := mustProp(t, cal, "current-user-privilege-set"); !strings.Contains(privs, "<d:all/>") || !strings.Contains(privs, "<d:write-content/>") {
		t.Fatalf("privileges %s", privs)
	}
	if v := mustProp(t, cal, "quota-available-bytes"); v != fmt.Sprint(1<<30) {
		t.Fatalf("quota-available-bytes %s", v)
	}
	ms = parseMS(t, e.as("PROPFIND", cardHome[0], map[string]string{"Depth": "1"}, propfindCollections))
	contacts := ms.get("/dav/addressbooks/alice@example.org/contacts/")
	if rt := mustProp(t, contacts, "resourcetype"); !strings.Contains(rt, "addressbook") {
		t.Fatalf("resourcetype %s", rt)
	}
	dir := ms.get("/dav/addressbooks/alice@example.org/directory/")
	privs := mustProp(t, dir, "current-user-privilege-set")
	if !strings.Contains(privs, "<d:read/>") || strings.Contains(privs, "write") || strings.Contains(privs, "<d:all/>") {
		t.Fatalf("directory privileges %s", privs)
	}
	// OPTIONS announces calendars and address books.
	r = e.do("OPTIONS", "/dav/", "", "", nil, "")
	if r.code != http.StatusOK || !strings.Contains(r.hdr.Get("DAV"), "calendar-access") || !strings.Contains(r.hdr.Get("DAV"), "addressbook") {
		t.Fatalf("OPTIONS %d %v", r.code, r.hdr)
	}
}

func TestSignIn(t *testing.T) {
	e := newEnv(t)
	body := `<d:propfind xmlns:d="DAV:"><d:prop><d:current-user-principal/></d:prop></d:propfind>`
	if r := e.do("PROPFIND", "/dav/", alice, "wrong", map[string]string{"Depth": "0"}, body); r.code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", r.code)
	}
	if r := e.do("PROPFIND", "/dav/", alice, e.app, map[string]string{"Depth": "0"}, body); r.code != http.StatusMultiStatus {
		t.Fatalf("app password: %d %s", r.code, r.body)
	}
	// Mail programs only with app passwords: the main password stops working.
	e.cfg.Mailboxes[0].AppPasswordsOnly = true
	if err := e.srv.Load(context.Background(), e.cfg); err != nil {
		t.Fatal(err)
	}
	if r := e.as("PROPFIND", "/dav/", map[string]string{"Depth": "0"}, body); r.code != http.StatusUnauthorized {
		t.Fatalf("main password with app passwords only: %d", r.code)
	}
	if r := e.do("PROPFIND", "/dav/", alice, e.app, map[string]string{"Depth": "0"}, body); r.code != http.StatusMultiStatus {
		t.Fatalf("app password with app passwords only: %d", r.code)
	}
	// Another host name is not served.
	req, _ := http.NewRequest("PROPFIND", e.ts.URL+"/dav/", nil)
	req.Host = "panel.example.net"
	req.SetBasicAuth(alice, e.app)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("other host: %d", res.StatusCode)
	}
	// Failures are throttled per network and address.
	e.ip = "198.51.100.7"
	var last resp
	for range 7 {
		last = e.do("PROPFIND", "/dav/", bob, "nope", map[string]string{"Depth": "0"}, body)
	}
	if last.code != http.StatusTooManyRequests {
		t.Fatalf("throttle: %d", last.code)
	}
}

func event(uid, extra string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20250101T000000Z\r\nDTSTART:20250310T090000Z\r\nDTEND:20250310T100000Z\r\nSUMMARY:Meeting\r\n" + extra + "END:VEVENT\r\nEND:VCALENDAR\r\n"
}

const calPath = "/dav/calendars/alice@example.org/calendar/"

func TestPutGetConditions(t *testing.T) {
	e := newEnv(t)
	ct := map[string]string{"Content-Type": "text/calendar; charset=utf-8", "If-None-Match": "*"}
	r := e.as("PUT", calPath+"one.ics", ct, event("one", ""))
	expect(t, r, http.StatusCreated)
	etag := r.hdr.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	// Creating it again fails.
	expect(t, e.as("PUT", calPath+"one.ics", ct, event("one", "")), http.StatusPreconditionFailed)
	g := e.as("GET", calPath+"one.ics", nil, "")
	expect(t, g, http.StatusOK, "UID:one")
	if g.hdr.Get("ETag") != etag || !strings.HasPrefix(g.hdr.Get("Content-Type"), "text/calendar") {
		t.Fatalf("GET %v", g.hdr)
	}
	if g.body != event("one", "") {
		t.Fatal("data changed")
	}
	if r := e.as("GET", calPath+"one.ics", map[string]string{"If-None-Match": etag}, ""); r.code != http.StatusNotModified {
		t.Fatalf("If-None-Match on GET: %d", r.code)
	}
	expect(t, e.as("PUT", calPath+"one.ics", map[string]string{"If-Match": `"nope"`}, event("one", "LOCATION:Here\r\n")), http.StatusPreconditionFailed)
	r = e.as("PUT", calPath+"one.ics", map[string]string{"If-Match": etag, "Content-Type": "text/calendar"}, event("one", "LOCATION:Here\r\n"))
	expect(t, r, http.StatusNoContent)
	etag2 := r.hdr.Get("ETag")
	if etag2 == "" || etag2 == etag {
		t.Fatalf("ETag after change %q (was %q)", etag2, etag)
	}
	expect(t, e.as("DELETE", calPath+"one.ics", map[string]string{"If-Match": etag}, ""), http.StatusPreconditionFailed)
	expect(t, e.as("DELETE", calPath+"one.ics", map[string]string{"If-Match": etag2}, ""), http.StatusNoContent)
	expect(t, e.as("GET", calPath+"one.ics", nil, ""), http.StatusNotFound)

	// Invalid objects are refused with the precondition.
	expect(t, e.as("PUT", calPath+"bad.ics", nil, "BEGIN:VCALENDAR\r\nhello"), http.StatusForbidden, "valid-calendar-data")
	expect(t, e.as("PUT", calPath+"two.ics", nil, strings.Replace(event("a", ""), "END:VCALENDAR", "BEGIN:VEVENT\r\nUID:b\r\nDTSTAMP:20250101T000000Z\r\nDTSTART:20250101T000000Z\r\nEND:VEVENT\r\nEND:VCALENDAR", 1)), http.StatusForbidden, "valid-calendar-object-resource")
	expect(t, e.as("PUT", calPath+"j.ics", nil, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nBEGIN:VJOURNAL\r\nUID:j\r\nDTSTAMP:20250101T000000Z\r\nEND:VJOURNAL\r\nEND:VCALENDAR\r\n"), http.StatusForbidden, "supported-calendar-component")
	expect(t, e.as("PUT", calPath+"x.ics", map[string]string{"Content-Type": "text/plain"}, event("x", "")), http.StatusUnsupportedMediaType)
	// One UID per collection.
	expect(t, e.as("PUT", calPath+"u1.ics", nil, event("same", "")), http.StatusCreated)
	expect(t, e.as("PUT", calPath+"u2.ics", nil, event("same", "")), http.StatusForbidden, "no-uid-conflict", calPath+"u1.ics")
	// Too large.
	big := event("big", "DESCRIPTION:"+strings.Repeat("x", 1<<20)+"\r\n")
	expect(t, e.as("PUT", calPath+"big.ics", nil, big), http.StatusRequestEntityTooLarge, "max-resource-size")
	// Tasks are calendar objects too.
	todo := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nBEGIN:VTODO\r\nUID:t1\r\nDTSTAMP:20250101T000000Z\r\nSUMMARY:Do it\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"
	expect(t, e.as("PUT", calPath+"t1.ics", nil, todo), http.StatusCreated)
}

func syncBody(token string) string {
	return `<?xml version="1.0"?><d:sync-collection xmlns:d="DAV:"><d:sync-token>` + token + `</d:sync-token><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`
}

func TestSyncCollection(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"a", "b", "c"} {
		expect(t, e.as("PUT", calPath+n+".ics", nil, event(n, "")), http.StatusCreated)
	}
	ms := parseMS(t, e.as("REPORT", calPath, nil, syncBody("")))
	if len(ms.Responses) != 3 || ms.SyncToken == "" {
		t.Fatalf("initial sync: %+v", ms)
	}
	tok := ms.SyncToken
	// Nothing changed.
	ms = parseMS(t, e.as("REPORT", calPath, nil, syncBody(tok)))
	if len(ms.Responses) != 0 || ms.SyncToken != tok {
		t.Fatalf("no changes: %+v", ms)
	}
	expect(t, e.as("PUT", calPath+"a.ics", nil, event("a", "LOCATION:x\r\n")), http.StatusNoContent)
	expect(t, e.as("DELETE", calPath+"b.ics", nil, ""), http.StatusNoContent)
	expect(t, e.as("PUT", calPath+"d.ics", nil, event("d", "")), http.StatusCreated)
	ms = parseMS(t, e.as("REPORT", calPath, nil, syncBody(tok)))
	if len(ms.Responses) != 3 {
		t.Fatalf("changes: %+v", ms)
	}
	if r := ms.get(calPath + "b.ics"); r == nil || !strings.Contains(r.Status, "404") {
		t.Fatalf("deleted: %+v", r)
	}
	if r := ms.get(calPath + "a.ics"); r == nil {
		t.Fatal("changed object missing")
	} else {
		mustProp(t, r, "getetag")
	}
	if ms.get(calPath+"d.ics") == nil || ms.get(calPath+"c.ics") != nil {
		t.Fatalf("changes: %+v", ms)
	}
	tok2 := ms.SyncToken
	if tok2 == tok {
		t.Fatal("token did not change")
	}
	// Tokens that are not ours, of another collection or from the future.
	for _, bad := range []string{"garbage", strings.Replace(tok2, ":sync:", ":sync:ff", 1), tok2[:len(tok2)-1] + "f0"} {
		expect(t, e.as("REPORT", calPath, nil, syncBody(bad)), http.StatusForbidden, "valid-sync-token")
	}
	// The ctag follows the changes.
	ms1 := parseMS(t, e.as("PROPFIND", calPath, map[string]string{"Depth": "0"}, propfindCollections))
	ctag := mustProp(t, ms1.get(calPath), "getctag")
	expect(t, e.as("DELETE", calPath+"d.ics", nil, ""), http.StatusNoContent)
	ms1 = parseMS(t, e.as("PROPFIND", calPath, map[string]string{"Depth": "0"}, propfindCollections))
	if mustProp(t, ms1.get(calPath), "getctag") == ctag {
		t.Fatal("ctag did not change")
	}
}

func calQuery(start, end string) string {
	return `<?xml version="1.0"?><c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><d:getetag/><c:calendar-data/></d:prop>
<c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT"><c:time-range start="` + start + `" end="` + end + `"/></c:comp-filter></c:comp-filter></c:filter></c:calendar-query>`
}

func queryNames(t *testing.T, e *env, start, end string) []string {
	t.Helper()
	ms := parseMS(t, e.as("REPORT", calPath, map[string]string{"Depth": "1"}, calQuery(start, end)))
	var out []string
	for _, r := range ms.Responses {
		out = append(out, strings.TrimPrefix(r.Href, calPath))
	}
	return out
}

const berlinTZ = "BEGIN:VTIMEZONE\r\nTZID:Europe/Berlin\r\nBEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nEND:STANDARD\r\nBEGIN:DAYLIGHT\r\nDTSTART:19700329T020000\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\nEND:DAYLIGHT\r\nEND:VTIMEZONE\r\n"

func calObj(uid, body string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\n" + berlinTZ + "BEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:20240101T000000Z\r\n" + body + "END:VEVENT\r\nEND:VCALENDAR\r\n"
}

func TestCalendarQueryTimeRange(t *testing.T) {
	e := newEnv(t)
	objs := map[string]string{
		// Every Monday 09:00-10:00 Berlin time from January 2024, forever.
		"weekly.ics": calObj("weekly", "DTSTART;TZID=Europe/Berlin:20240101T090000\r\nDTEND;TZID=Europe/Berlin:20240101T100000\r\nRRULE:FREQ=WEEKLY;BYDAY=MO\r\n"),
		// Daily, five times from 2024-03-01, one excluded.
		"five.ics": calObj("five", "DTSTART:20240301T120000Z\r\nDURATION:PT1H\r\nRRULE:FREQ=DAILY;COUNT=5\r\nEXDATE:20240303T120000Z\r\n"),
		// A floating evening (no time zone): matches wherever the reader is.
		"floating.ics": calObj("floating", "DTSTART:20240615T200000\r\nDTEND:20240615T210000\r\n"),
		// An all-day event.
		"allday.ics": calObj("allday", "DTSTART;VALUE=DATE:20240720\r\n"),
		// A time zone known only by its Windows name.
		"windows.ics": "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nBEGIN:VEVENT\r\nUID:windows\r\nDTSTAMP:20240101T000000Z\r\nDTSTART;TZID=W. Europe Standard Time:20240806T090000\r\nDTEND;TZID=W. Europe Standard Time:20240806T100000\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
	}
	for name, body := range objs {
		expect(t, e.as("PUT", calPath+name, nil, body), http.StatusCreated)
	}
	cases := []struct {
		start, end string
		want       []string
	}{
		// A Monday in 2026: 09:00 Berlin (summer) is 07:00 UTC.
		{"20260608T065900Z", "20260608T070100Z", []string{"weekly.ics"}},
		// The same Monday before the event (08:00 Berlin is 06:00 UTC).
		{"20260608T050000Z", "20260608T060000Z", nil},
		// Before the series started.
		{"20231201T000000Z", "20231225T000000Z", nil},
		// The excluded day, then a day of the series, then after the end.
		{"20240303T000000Z", "20240303T235959Z", nil},
		{"20240304T110000Z", "20240304T130000Z", []string{"five.ics"}},
		{"20240310T000000Z", "20240310T235959Z", nil},
		{"20240615T190000Z", "20240615T193000Z", []string{"floating.ics"}},
		{"20240720T100000Z", "20240720T110000Z", []string{"allday.ics"}},
		{"20240721T150000Z", "20240721T200000Z", nil},
		{"20240806T073000Z", "20240806T074500Z", []string{"windows.ics"}},
		// 09:00 in that zone is not 09:00 UTC.
		{"20240806T090000Z", "20240806T093000Z", nil},
	}
	for _, c := range cases {
		got := queryNames(t, e, c.start, c.end)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s..%s: got %v, want %v", c.start, c.end, got, c.want)
		}
	}
	// Results carry the data.
	ms := parseMS(t, e.as("REPORT", calPath, map[string]string{"Depth": "1"}, calQuery("20240304T110000Z", "20240304T130000Z")))
	if d := mustProp(t, &ms.Responses[0], "calendar-data"); !strings.Contains(d, "UID:five") {
		t.Fatalf("calendar-data %q", d)
	}
}

func TestMultigetAndPropfindObjects(t *testing.T) {
	e := newEnv(t)
	expect(t, e.as("PUT", calPath+"a.ics", nil, event("a", "")), http.StatusCreated)
	body := `<?xml version="1.0"?><c:calendar-multiget xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><d:getetag/><c:calendar-data/></d:prop>
<d:href>` + calPath + `a.ics</d:href><d:href>` + calPath + `missing.ics</d:href><d:href>/dav/calendars/bob@example.org/calendar/x.ics</d:href></c:calendar-multiget>`
	ms := parseMS(t, e.as("REPORT", calPath, map[string]string{"Depth": "1"}, body))
	if d := mustProp(t, ms.get(calPath+"a.ics"), "calendar-data"); !strings.Contains(d, "UID:a") {
		t.Fatal(d)
	}
	if r := ms.get(calPath + "missing.ics"); r == nil || !strings.Contains(r.Status, "404") {
		t.Fatalf("missing: %+v", r)
	}
	if r := ms.get("/dav/calendars/bob@example.org/calendar/x.ics"); r == nil || !strings.Contains(r.Status, "404") {
		t.Fatalf("other mailbox: %+v", r)
	}
	ms = parseMS(t, e.as("PROPFIND", calPath, map[string]string{"Depth": "1"}, `<d:propfind xmlns:d="DAV:"><d:prop><d:getetag/><d:getcontenttype/></d:prop></d:propfind>`))
	if ct := mustProp(t, ms.get(calPath+"a.ics"), "getcontenttype"); !strings.Contains(ct, "component=vevent") {
		t.Fatal(ct)
	}
	// Depth infinity is refused on collections.
	expect(t, e.as("PROPFIND", calPath, map[string]string{"Depth": "infinity"}, ""), http.StatusForbidden, "propfind-finite-depth")
}

func TestOtherMailboxesHidden(t *testing.T) {
	e := newEnv(t)
	r := e.do("PUT", "/dav/calendars/bob@example.org/calendar/b.ics", bob, bobPW, nil, event("b", ""))
	expect(t, r, http.StatusCreated)
	for _, p := range []string{"/dav/principals/bob@example.org/", "/dav/calendars/bob@example.org/", "/dav/calendars/bob@example.org/calendar/", "/dav/addressbooks/bob@example.org/directory/"} {
		expect(t, e.as("PROPFIND", p, map[string]string{"Depth": "0"}, ""), http.StatusNotFound)
	}
	expect(t, e.as("GET", "/dav/calendars/bob@example.org/calendar/b.ics", nil, ""), http.StatusNotFound)
	expect(t, e.as("PUT", "/dav/calendars/bob@example.org/calendar/x.ics", nil, event("x", "")), http.StatusNotFound)
	expect(t, e.as("DELETE", "/dav/calendars/bob@example.org/calendar/b.ics", nil, ""), http.StatusNotFound)
	expect(t, e.as("MKCALENDAR", "/dav/calendars/bob@example.org/new/", nil, ""), http.StatusNotFound)
	// Paths that try to leave the tree are cleaned first, never served.
	if r := e.as("GET", "/dav/calendars/alice@example.org/../bob@example.org/calendar/b.ics", nil, ""); r.code == http.StatusOK {
		t.Fatal("served another mailbox's object")
	}
}

func TestCollectionsAndProperties(t *testing.T) {
	e := newEnv(t)
	mk := `<?xml version="1.0"?><c:mkcalendar xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:a="http://apple.com/ns/ical/"><d:set><d:prop>
<d:displayname>Work</d:displayname><a:calendar-color>#FF0000FF</a:calendar-color><c:supported-calendar-component-set><c:comp name="VTODO"/></c:supported-calendar-component-set></d:prop></d:set></c:mkcalendar>`
	expect(t, e.as("MKCALENDAR", "/dav/calendars/alice@example.org/work/", nil, mk), http.StatusCreated)
	expect(t, e.as("MKCALENDAR", "/dav/calendars/alice@example.org/work/", nil, ""), http.StatusMethodNotAllowed)
	// It takes tasks only.
	expect(t, e.as("PUT", "/dav/calendars/alice@example.org/work/e.ics", nil, event("e", "")), http.StatusForbidden, "supported-calendar-component")
	patch := `<?xml version="1.0"?><d:propertyupdate xmlns:d="DAV:" xmlns:a="http://apple.com/ns/ical/"><d:set><d:prop><d:displayname>Work stuff</d:displayname><a:calendar-color>#00FF00</a:calendar-color><a:calendar-order>3</a:calendar-order></d:prop></d:set></d:propertyupdate>`
	ms := parseMS(t, e.as("PROPPATCH", "/dav/calendars/alice@example.org/work/", nil, patch))
	if len(ms.Responses) != 1 || strings.Contains(e.as("PROPPATCH", "/dav/calendars/alice@example.org/work/", nil, patch).body, "403") {
		t.Fatalf("proppatch %+v", ms)
	}
	ms = parseMS(t, e.as("PROPFIND", "/dav/calendars/alice@example.org/work/", map[string]string{"Depth": "0"}, propfindCollections))
	w := ms.get("/dav/calendars/alice@example.org/work/")
	if mustProp(t, w, "displayname") != "Work stuff" || mustProp(t, w, "calendar-color") != "#00FF00" {
		t.Fatalf("%+v", w)
	}
	// An unknown property fails the whole update.
	bad := `<d:propertyupdate xmlns:d="DAV:" xmlns:x="urn:x"><d:set><d:prop><d:displayname>No</d:displayname><x:thing>1</x:thing></d:prop></d:set></d:propertyupdate>`
	r := e.as("PROPPATCH", "/dav/calendars/alice@example.org/work/", nil, bad)
	expect(t, r, http.StatusMultiStatus, "403", "424")
	// Address books come with extended MKCOL.
	mkcol := `<?xml version="1.0"?><d:mkcol xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav"><d:set><d:prop><d:resourcetype><d:collection/><card:addressbook/></d:resourcetype><d:displayname>Friends</d:displayname></d:prop></d:set></d:mkcol>`
	expect(t, e.as("MKCOL", "/dav/addressbooks/alice@example.org/friends/", nil, mkcol), http.StatusCreated)
	expect(t, e.as("MKCOL", "/dav/addressbooks/alice@example.org/plain/", nil, ""), http.StatusForbidden)
	expect(t, e.as("MKCOL", "/dav/addressbooks/alice@example.org/directory/", nil, mkcol), http.StatusMethodNotAllowed)
	expect(t, e.as("DELETE", "/dav/addressbooks/alice@example.org/friends/", nil, ""), http.StatusNoContent)
	expect(t, e.as("DELETE", "/dav/addressbooks/alice@example.org/directory/", nil, ""), http.StatusForbidden)
	// At most MaxDAVCollections collections.
	for i := 0; ; i++ {
		r := e.as("MKCALENDAR", fmt.Sprintf("/dav/calendars/alice@example.org/c%d/", i), nil, "")
		if r.code == http.StatusInsufficientStorage {
			if i+3 != storage.MaxDAVCollections { // calendar, contacts, work
				t.Fatalf("limit after %d", i)
			}
			break
		}
		expect(t, r, http.StatusCreated)
	}
}

func vcard(uid, fn, email string) string {
	return "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:" + uid + "\r\nFN:" + fn + "\r\nN:" + fn + ";;;;\r\nEMAIL:" + email + "\r\nEND:VCARD\r\n"
}

const bookPath = "/dav/addressbooks/alice@example.org/contacts/"

func TestContacts(t *testing.T) {
	e := newEnv(t)
	expect(t, e.as("PUT", bookPath+"x.vcf", map[string]string{"Content-Type": "text/vcard"}, vcard("x", "Xavier Doe", "x@example.com")), http.StatusCreated)
	expect(t, e.as("PUT", bookPath+"y.vcf", nil, vcard("y", "Yvonne Roe", "y@example.com")), http.StatusCreated)
	expect(t, e.as("PUT", bookPath+"z.vcf", nil, "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:No UID\r\nEND:VCARD\r\n"), http.StatusForbidden, "valid-address-data")
	q := `<?xml version="1.0"?><card:addressbook-query xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav"><d:prop><d:getetag/><card:address-data/></d:prop>
<card:filter><card:prop-filter name="FN"><card:text-match match-type="contains">yvonne</card:text-match></card:prop-filter></card:filter></card:addressbook-query>`
	ms := parseMS(t, e.as("REPORT", bookPath, map[string]string{"Depth": "1"}, q))
	if len(ms.Responses) != 1 || !strings.Contains(mustProp(t, &ms.Responses[0], "address-data"), "Yvonne") {
		t.Fatalf("%+v", ms)
	}
	mg := `<card:addressbook-multiget xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav"><d:prop><d:getetag/></d:prop><d:href>` + bookPath + `x.vcf</d:href></card:addressbook-multiget>`
	ms = parseMS(t, e.as("REPORT", bookPath, nil, mg))
	mustProp(t, ms.get(bookPath+"x.vcf"), "getetag")
	ms = parseMS(t, e.as("REPORT", bookPath, nil, syncBody("")))
	if len(ms.Responses) != 2 {
		t.Fatalf("%+v", ms)
	}
}

func TestDirectory(t *testing.T) {
	e := newEnv(t)
	dirPath := "/dav/addressbooks/alice@example.org/directory/"
	all := func(user, pw string) (string, []string) {
		path := strings.Replace(dirPath, alice, user, 1)
		r := e.do("REPORT", path, user, pw, map[string]string{"Depth": "1"},
			`<card:addressbook-query xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav"><d:prop><d:getetag/><card:address-data/></d:prop></card:addressbook-query>`)
		ms := parseMS(t, r)
		var data []string
		for _, resp := range ms.Responses {
			data = append(data, mustProp(t, &resp, "address-data")+mustProp(t, &resp, "getetag"))
		}
		return strings.Join(data, "\n"), data
	}
	text, cards := all(alice, alicePW)
	for _, want := range []string{"EMAIL;TYPE=INTERNET:alice@example.org", "FN:Alice Example", "bob@example.org", "carol@example.de", "FN:Carol", "info@example.org", "team@example.org"} {
		if !strings.Contains(text, want) {
			t.Errorf("directory lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "dave@other.test") || strings.Contains(text, "*@") {
		t.Errorf("directory shows another organisation or the catch-all:\n%s", text)
	}
	if len(cards) != 5 {
		t.Errorf("%d cards", len(cards))
	}
	// Another organisation sees only itself.
	text2, _ := all(dave, davePW)
	if !strings.Contains(text2, "dave@other.test") || strings.Contains(text2, "alice") {
		t.Errorf("other organisation:\n%s", text2)
	}
	// ETags stay the same for the same configuration.
	again, _ := all(alice, alicePW)
	if again != text {
		t.Error("directory changed without a change")
	}
	if err := e.srv.Load(context.Background(), e.cfg); err != nil {
		t.Fatal(err)
	}
	again, _ = all(alice, alicePW)
	if again != text {
		t.Error("directory changed after the same configuration was loaded")
	}
	// Read only.
	expect(t, e.as("PUT", dirPath+"new.vcf", nil, vcard("n", "N", "n@example.org")), http.StatusForbidden)
	// Sync: the current token gives no changes, an old one a full sync.
	ms := parseMS(t, e.as("REPORT", dirPath, nil, syncBody("")))
	tok := ms.SyncToken
	if len(ms.Responses) != 5 || tok == "" {
		t.Fatalf("%+v", ms)
	}
	ms = parseMS(t, e.as("REPORT", dirPath, nil, syncBody(tok)))
	if len(ms.Responses) != 0 {
		t.Fatalf("%+v", ms)
	}
	e.cfg.Mailboxes[1].DisplayName = "Bob Builder"
	if err := e.srv.Load(context.Background(), e.cfg); err != nil {
		t.Fatal(err)
	}
	expect(t, e.as("REPORT", dirPath, nil, syncBody(tok)), http.StatusForbidden, "valid-sync-token")
	text3, _ := all(alice, alicePW)
	if !strings.Contains(text3, "FN:Bob Builder") || !strings.Contains(text3, "N:Builder;Bob;;;") {
		t.Errorf("renamed:\n%s", text3)
	}
}

func TestQuota(t *testing.T) {
	e := newEnv(t)
	e.cfg.Mailboxes[0].QuotaBytes = 2000
	if err := e.srv.Load(context.Background(), e.cfg); err != nil {
		t.Fatal(err)
	}
	expect(t, e.as("PUT", calPath+"a.ics", nil, event("a", "")), http.StatusCreated)
	big := event("b", "DESCRIPTION:"+strings.Repeat("x", 2000)+"\r\n")
	expect(t, e.as("PUT", calPath+"b.ics", nil, big), http.StatusInsufficientStorage, "quota-not-exceeded")
	u, err := e.srv.Store().Usage(context.Background(), mailtest.ID('a'))
	if err != nil || u.Bytes != int64(len(event("a", ""))) {
		t.Fatalf("usage %+v %v", u, err)
	}
	ms := parseMS(t, e.as("PROPFIND", calPath, map[string]string{"Depth": "0"}, propfindCollections))
	if v := mustProp(t, ms.get(calPath), "quota-used-bytes"); v != fmt.Sprint(u.Bytes) {
		t.Fatalf("quota-used-bytes %s", v)
	}
	// Deleting gives the room back.
	expect(t, e.as("DELETE", calPath+"a.ics", nil, ""), http.StatusNoContent)
	if u, _ := e.srv.Store().Usage(context.Background(), mailtest.ID('a')); u.Bytes != 0 {
		t.Fatalf("usage after delete %+v", u)
	}
}

func TestXMLLimits(t *testing.T) {
	e := newEnv(t)
	deep := `<d:propfind xmlns:d="DAV:">` + strings.Repeat("<d:x>", 100) + strings.Repeat("</d:x>", 100) + `</d:propfind>`
	expect(t, e.as("PROPFIND", calPath, map[string]string{"Depth": "0"}, deep), http.StatusRequestEntityTooLarge)
	dtd := `<?xml version="1.0"?><!DOCTYPE x [<!ENTITY a "aaaa">]><d:propfind xmlns:d="DAV:"><d:prop><d:getetag/></d:prop></d:propfind>`
	expect(t, e.as("PROPFIND", calPath, map[string]string{"Depth": "0"}, dtd), http.StatusBadRequest)
	expect(t, e.as("PROPFIND", calPath, map[string]string{"Depth": "0"}, "<d:propfind xmlns:d='DAV:'><d:prop>"), http.StatusBadRequest)
	huge := `<d:propfind xmlns:d="DAV:"><d:prop>` + strings.Repeat("<d:getetag/>", 30000) + `</d:prop></d:propfind>`
	expect(t, e.as("PROPFIND", calPath, map[string]string{"Depth": "0"}, huge), http.StatusRequestEntityTooLarge)
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	e.h.opts.Now = func() time.Time { return now }
	for i := 0; i < RateLimit; i++ {
		if !e.h.allow(mailtest.ID('a')) {
			t.Fatalf("refused after %d", i)
		}
	}
	r := e.as("PROPFIND", "/dav/", map[string]string{"Depth": "0"}, "")
	if r.code != http.StatusTooManyRequests || r.hdr.Get("Retry-After") == "" {
		t.Fatalf("%d %v", r.code, r.hdr)
	}
	now = now.Add(RateWindow)
	expect(t, e.as("PROPFIND", "/dav/", map[string]string{"Depth": "0"}, ""), http.StatusMultiStatus)
}

// TestRestoredEpoch: after a restore (the marker file), every ETag, ctag
// and sync token changes, so programs sync everything again.
func TestRestoredEpoch(t *testing.T) {
	e := newEnv(t)
	r := e.as("PUT", calPath+"a.ics", nil, event("a", ""))
	expect(t, r, http.StatusCreated)
	etag := r.hdr.Get("ETag")
	tok := parseMS(t, e.as("REPORT", calPath, nil, syncBody(""))).SyncToken
	// A plain restart keeps them.
	e.restart()
	if g := e.as("GET", calPath+"a.ics", nil, ""); g.hdr.Get("ETag") != etag {
		t.Fatalf("ETag changed on restart: %s %s", g.hdr.Get("ETag"), etag)
	}
	expect(t, e.as("REPORT", calPath, nil, syncBody(tok)), http.StatusMultiStatus)
	if err := os.WriteFile(filepath.Join(e.dir, storage.RestoredFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e.restart()
	g := e.as("GET", calPath+"a.ics", nil, "")
	expect(t, g, http.StatusOK, "UID:a")
	if g.hdr.Get("ETag") == etag {
		t.Fatal("ETag kept after a restore")
	}
	expect(t, e.as("REPORT", calPath, nil, syncBody(tok)), http.StatusForbidden, "valid-sync-token")
}

func TestImportExport(t *testing.T) {
	e := newEnv(t)
	st := e.srv.Store()
	ctx := context.Background()
	if err := st.EnsureDefaultDAVCollections(ctx, mailtest.ID('a')); err != nil {
		t.Fatal(err)
	}
	c, err := st.DAVCollection(ctx, mailtest.ID('a'), "calendar", storage.DefaultCalendar)
	if err != nil {
		t.Fatal(err)
	}
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\n" + berlinTZ +
		"BEGIN:VEVENT\r\nUID:one\r\nDTSTAMP:20240101T000000Z\r\nDTSTART;TZID=Europe/Berlin:20240101T090000\r\nRRULE:FREQ=DAILY\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:one\r\nRECURRENCE-ID;TZID=Europe/Berlin:20240102T090000\r\nDTSTAMP:20240101T000000Z\r\nDTSTART;TZID=Europe/Berlin:20240102T110000\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nDTSTAMP:20240101T000000Z\r\nDTSTART:20240105T090000Z\r\nSUMMARY:No UID\r\nEND:VEVENT\r\n" +
		"BEGIN:VJOURNAL\r\nUID:j\r\nDTSTAMP:20240101T000000Z\r\nEND:VJOURNAL\r\nEND:VCALENDAR\r\n"
	res, err := Import(ctx, st, c, []byte(ics))
	if err != nil || res.Added != 2 || res.Skipped != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	res, err = Import(ctx, st, c, []byte(ics))
	if err != nil || res.Replaced != 1 || res.Added != 1 {
		t.Fatalf("again: %+v %v", res, err)
	}
	var out strings.Builder
	if err := Export(ctx, st, c, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if strings.Count(s, "BEGIN:VTIMEZONE") != 1 || strings.Count(s, "BEGIN:VEVENT") != 4 || !strings.Contains(s, "RECURRENCE-ID") {
		t.Fatalf("export:\n%s", s)
	}
	if _, err := Import(ctx, st, c, []byte("not a calendar")); err == nil {
		t.Fatal("garbage imported")
	}
	// The exported file reads back.
	res, err = Import(ctx, st, c, []byte(s))
	if err != nil || res.Replaced != 3 {
		t.Fatalf("re-import: %+v %v", res, err)
	}
	// GET on the collection downloads it.
	g := e.as("GET", calPath, nil, "")
	expect(t, g, http.StatusOK, "BEGIN:VCALENDAR", "UID:one")
	if !strings.Contains(g.hdr.Get("Content-Disposition"), "Calendar.ics") {
		t.Fatal(g.hdr)
	}
}
