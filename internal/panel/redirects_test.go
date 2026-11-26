package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/dns/hetzner/hetznertest"
	"github.com/danielgtmn/ownpanel/internal/store"
)

var hetznerUniqueness = hetznertest.Error{Status: 409, Code: "uniqueness_error", Message: "zone with this name already exists"}

func storeRedirect(i int) store.Redirect {
	return store.Redirect{OwnerID: admin.UserID, SourceDomain: fmt.Sprintf("r%d.example.com", i), Target: "https://example.com", Code: 301}
}

func TestRedirectValidation(t *testing.T) {
	svc, _, _ := newService(t)
	svc.settings.panelDomain = "panel.example.com"
	cases := []struct {
		name string
		in   RedirectInput
		want string // "" valid; else part of the error
	}{
		{"domain redirect", RedirectInput{SourceDomain: " WWW.Example.com. ", Target: "https://example.com", Code: 301, KeepPath: true, KeepQuery: true}, ""},
		{"exact path", RedirectInput{SourceDomain: "example.com", SourcePath: "/old.html", Target: "/new.html", Code: 302}, ""},
		{"prefix", RedirectInput{SourceDomain: "example.com", SourcePath: "/blog/*", Target: "https://blog.example.com/", Code: 308, KeepPath: true}, ""},
		{"every path", RedirectInput{SourceDomain: "example.com", SourcePath: "/*", Target: "https://example.org", Code: 307}, ""},
		{"non-ASCII path", RedirectInput{SourceDomain: "example.com", SourcePath: "/über", Target: "/ueber", Code: 301}, ""},
		{"target with query", RedirectInput{SourceDomain: "example.com", SourcePath: "/a", Target: "https://example.org/?ref=a", Code: 301}, ""},
		{"same host other path", RedirectInput{SourceDomain: "example.com", SourcePath: "/a", Target: "https://example.com/b", Code: 301}, ""},
		{"other port is no loop", RedirectInput{SourceDomain: "a.example.com", Target: "https://a.example.com:8443/", Code: 301}, ""},

		{"no domain", RedirectInput{Target: "https://example.com", Code: 301}, "enter the domain"},
		{"IPv4", RedirectInput{SourceDomain: "203.0.113.5", Target: "https://example.com", Code: 301}, "not an IP address"},
		{"IPv6", RedirectInput{SourceDomain: "[2001:db8::1]", Target: "https://example.com", Code: 301}, "not an IP address"},
		{"bad domain", RedirectInput{SourceDomain: "exa mple.com", Target: "https://example.com", Code: 301}, "not a valid domain"},
		{"single label", RedirectInput{SourceDomain: "localhost", Target: "https://example.com", Code: 301}, "not a valid domain"},
		{"panel domain", RedirectInput{SourceDomain: "panel.example.com", Target: "https://example.com", Code: 301}, "the panel itself"},
		{"code 200", RedirectInput{SourceDomain: "a.example.com", Target: "https://example.com", Code: 200}, "301, 302, 307 or 308"},
		{"code missing", RedirectInput{SourceDomain: "a.example.com", Target: "https://example.com"}, "301, 302, 307 or 308"},
		{"code 303", RedirectInput{SourceDomain: "a.example.com", Target: "https://example.com", Code: 303}, "301, 302, 307 or 308"},
		{"no target", RedirectInput{SourceDomain: "a.example.com", Code: 301}, "where the redirect should lead"},
		{"relative target", RedirectInput{SourceDomain: "a.example.com", Target: "example.com", Code: 301}, "full address"},
		{"ftp target", RedirectInput{SourceDomain: "a.example.com", Target: "ftp://example.com/", Code: 301}, "full address"},
		{"javascript", RedirectInput{SourceDomain: "a.example.com", SourcePath: "/x", Target: "javascript:alert(1)", Code: 301}, "not a valid target"},
		{"domain to a path", RedirectInput{SourceDomain: "a.example.com", Target: "/new", Code: 301}, "not to a path"},
		{"protocol-relative", RedirectInput{SourceDomain: "a.example.com", SourcePath: "/x", Target: "//evil.example/", Code: 301}, "must not start with //"},
		{"user in target", RedirectInput{SourceDomain: "a.example.com", Target: "https://u:p@example.com/", Code: 301}, "user name or password"},
		{"space in target", RedirectInput{SourceDomain: "a.example.com", Target: "https://example.com/a b", Code: 301}, "spaces"},
		{"brace in target", RedirectInput{SourceDomain: "a.example.com", Target: "https://example.com/{x}", Code: 301}, "braces"},
		{"keep path with query", RedirectInput{SourceDomain: "a.example.com", Target: "https://example.com/?a=1", Code: 301, KeepPath: true}, "with ? or #"},
		{"keep query with query", RedirectInput{SourceDomain: "a.example.com", SourcePath: "/x", Target: "/y?a=1", Code: 301, KeepQuery: true}, "with ? or #"},
		{"path without slash", RedirectInput{SourceDomain: "a.example.com", SourcePath: "old", Target: "/new", Code: 301}, "must start with /"},
		{"path with query", RedirectInput{SourceDomain: "a.example.com", SourcePath: "/old?x=1", Target: "/new", Code: 301}, "? or #"},
		{"path with percent", RedirectInput{SourceDomain: "a.example.com", SourcePath: "/a%20b", Target: "/new", Code: 301}, "percent-encoding"},
		{"star inside", RedirectInput{SourceDomain: "a.example.com", SourcePath: "/a*/b", Target: "/new", Code: 301}, "can only end in /*"},
		{"star without slash", RedirectInput{SourceDomain: "a.example.com", SourcePath: "/blog*", Target: "/new", Code: 301}, "can only end in /*"},
		{"dot segment", RedirectInput{SourceDomain: "a.example.com", SourcePath: "/a/../b", Target: "/new", Code: 301}, ". or .."},
		{"double slash", RedirectInput{SourceDomain: "a.example.com", SourcePath: "/a//b", Target: "/new", Code: 301}, "//"},
		{"long path", RedirectInput{SourceDomain: "a.example.com", SourcePath: "/" + strings.Repeat("a", 1024), Target: "/new", Code: 301}, "at most 1024"},
		{"long target", RedirectInput{SourceDomain: "a.example.com", Target: "https://example.com/" + strings.Repeat("a", 2048), Code: 301}, "at most 2048"},

		{"loop: domain to itself", RedirectInput{SourceDomain: "a.example.com", Target: "https://A.example.com/x", Code: 301}, "redirect again forever"},
		{"loop: domain to itself over http", RedirectInput{SourceDomain: "a.example.com", Target: "http://a.example.com:80", Code: 301}, "redirect again forever"},
		{"loop: exact path", RedirectInput{SourceDomain: "a.example.com", SourcePath: "/a", Target: "/a?x=1", Code: 301}, "redirect again forever"},
		{"loop: exact path absolute", RedirectInput{SourceDomain: "a.example.com", SourcePath: "/a", Target: "https://a.example.com/a", Code: 301}, "redirect again forever"},
		{"loop: into the prefix", RedirectInput{SourceDomain: "a.example.com", SourcePath: "/blog/*", Target: "/blog/new", Code: 301}, "redirect again forever"},
		{"loop: prefix base", RedirectInput{SourceDomain: "a.example.com", SourcePath: "/blog/*", Target: "/blog", Code: 301}, "redirect again forever"},
		{"loop: every path", RedirectInput{SourceDomain: "a.example.com", SourcePath: "/*", Target: "/", Code: 301}, "redirect again forever"},
		{"no loop: sibling prefix", RedirectInput{SourceDomain: "a.example.com", SourcePath: "/blog/*", Target: "/blogger", Code: 301}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := svc.validRedirect(c.in)
			if c.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted: %+v", r)
			}
			wantKind(t, err, Invalid)
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not mention %q", err, c.want)
			}
		})
	}
	r, _ := svc.validRedirect(cases[0].in)
	if r.SourceDomain != "www.example.com" || r.SourcePath != "" || r.Target != "https://example.com" {
		t.Fatalf("normalized: %+v", r)
	}
}

// redirectWorld is the DNS world plus redirects: bob's domain redirect
// bobold.example.com, root's rootold.example.com, and bob's path redirect
// /old on bobsite. Both Hetzner projects also hold an unused zone spare.com.
type redirectWorld struct {
	*dnsWorld
	bobDomain, rootDomain, bobPath int64
}

func newRedirectWorld(t *testing.T) *redirectWorld {
	t.Helper()
	d := newDNSWorld(t)
	ctx := context.Background()
	w := &redirectWorld{dnsWorld: d}
	mk := func(a Actor, in RedirectInput) int64 {
		t.Helper()
		r, err := d.svc.CreateRedirect(ctx, a, in)
		if err != nil {
			t.Fatal(err)
		}
		return mustStoreID(r.ID)
	}
	w.bobDomain = mk(bob, RedirectInput{SourceDomain: "bobold.example.com", Target: "https://bobsite.example.com", Code: 301})
	w.rootDomain = mk(admin, RedirectInput{SourceDomain: "rootold.example.com", Target: "https://rootsite.example.com", Code: 302})
	w.bobPath = mk(bob, RedirectInput{SourceDomain: "bobsite.example.com", SourcePath: "/old", Target: "/new", Code: 301})
	for _, tok := range []string{rootToken, bobToken} {
		d.hz[tok].AddZone("spare.com", "primary")
	}
	d.svc.dnsCache.dropZones(d.rootConn)
	d.svc.dnsCache.dropZones(d.bobConn)
	return w
}

func TestRedirectPermissionMatrix(t *testing.T) {
	ctx := context.Background()
	actors := []struct {
		name string
		a    Actor
	}{
		{"admin", admin}, {"admin write token", rootWriteTok}, {"member", bob}, {"member read token", bobRead},
		{"member site token", bobLimited}, {"admin read token", rootRead}, {"admin site token", rootLimited},
		{"nobody", nobody},
	}
	F, N, U, I := Forbidden, NotFound, Unauthenticated, Invalid
	upd := func(domain string) RedirectInput {
		return RedirectInput{SourceDomain: domain, Target: "https://changed.example.org", Code: 308}
	}
	ops := []struct {
		name string
		run  func(w *redirectWorld, a Actor) error
		want []Kind
	}{
		{"site redirects", func(w *redirectWorld, a Actor) error { _, err := w.svc.SiteRedirects(ctx, a, "bobsite"); return err },
			[]Kind{ok, ok, ok, ok, ok, ok, N, U}},
		{"get bob's domain redirect", func(w *redirectWorld, a Actor) error {
			_, err := w.svc.Redirect(ctx, a, idString(w.bobDomain))
			return err
		},
			[]Kind{ok, ok, ok, ok, N, ok, N, U}},
		{"get bob's path redirect", func(w *redirectWorld, a Actor) error {
			_, err := w.svc.Redirect(ctx, a, idString(w.bobPath))
			return err
		},
			[]Kind{ok, ok, ok, ok, ok, ok, N, U}},
		{"create path redirect", func(w *redirectWorld, a Actor) error {
			_, err := w.svc.CreateRedirect(ctx, a, RedirectInput{SourceDomain: "bobsite.example.com", SourcePath: "/x", Target: "/y", Code: 301})
			return err
		}, []Kind{ok, ok, ok, F, ok, F, I, U}},
		{"create path redirect on another site", func(w *redirectWorld, a Actor) error {
			_, err := w.svc.CreateRedirect(ctx, a, RedirectInput{SourceDomain: "bobother.example.com", SourcePath: "/x", Target: "/y", Code: 301})
			return err
		}, []Kind{ok, ok, ok, F, I, F, I, U}},
		{"create path redirect on admin's site", func(w *redirectWorld, a Actor) error {
			_, err := w.svc.CreateRedirect(ctx, a, RedirectInput{SourceDomain: "rootsite.example.com", SourcePath: "/x", Target: "/y", Code: 301})
			return err
		}, []Kind{ok, ok, I, F, I, F, ok, U}},
		{"create domain redirect", func(w *redirectWorld, a Actor) error {
			_, err := w.svc.CreateRedirect(ctx, a, RedirectInput{SourceDomain: "fresh.example.org", Target: "https://example.org", Code: 301})
			return err
		}, []Kind{ok, ok, ok, F, F, F, F, U}},
		{"update bob's domain redirect", func(w *redirectWorld, a Actor) error {
			_, err := w.svc.UpdateRedirect(ctx, a, idString(w.bobDomain), upd("bobold.example.com"))
			return err
		}, []Kind{ok, ok, ok, F, N, F, N, U}},
		{"update root's domain redirect", func(w *redirectWorld, a Actor) error {
			_, err := w.svc.UpdateRedirect(ctx, a, idString(w.rootDomain), upd("rootold.example.com"))
			return err
		}, []Kind{ok, ok, N, N, N, F, N, U}},
		{"update bob's path redirect", func(w *redirectWorld, a Actor) error {
			_, err := w.svc.UpdateRedirect(ctx, a, idString(w.bobPath), RedirectInput{SourceDomain: "bobsite.example.com", SourcePath: "/older", Target: "/new", Code: 302})
			return err
		}, []Kind{ok, ok, ok, F, ok, F, N, U}},
		{"turn a path redirect into a domain redirect", func(w *redirectWorld, a Actor) error {
			_, err := w.svc.UpdateRedirect(ctx, a, idString(w.bobPath), upd("another.example.org"))
			return err
		}, []Kind{ok, ok, ok, F, F, F, N, U}},
		{"delete bob's path redirect", func(w *redirectWorld, a Actor) error { return w.svc.DeleteRedirect(ctx, a, idString(w.bobPath)) },
			[]Kind{ok, ok, ok, F, ok, F, N, U}},
		{"delete bob's domain redirect", func(w *redirectWorld, a Actor) error { return w.svc.DeleteRedirect(ctx, a, idString(w.bobDomain)) },
			[]Kind{ok, ok, ok, F, N, F, N, U}},
		{"delete root's domain redirect", func(w *redirectWorld, a Actor) error { return w.svc.DeleteRedirect(ctx, a, idString(w.rootDomain)) },
			[]Kind{ok, ok, N, N, N, F, N, U}},
		{"status of a redirect domain", func(w *redirectWorld, a Actor) error {
			_, err := w.svc.DomainStatusOf(ctx, a, "bobold.example.com")
			return err
		}, []Kind{ok, ok, ok, ok, N, ok, N, U}},
		{"status of a site domain", func(w *redirectWorld, a Actor) error {
			_, err := w.svc.DomainStatusOf(ctx, a, "bobsite.example.com")
			return err
		}, []Kind{ok, ok, ok, ok, ok, ok, N, U}},
		{"status of an unknown domain", func(w *redirectWorld, a Actor) error {
			_, err := w.svc.DomainStatusOf(ctx, a, "unknown.example.com")
			return err
		}, []Kind{N, N, N, N, N, N, N, U}},
		{"point a redirect domain", func(w *redirectWorld, a Actor) error {
			_, err := w.svc.PointDomainOf(ctx, a, "bobold.example.com")
			return err
		}, []Kind{ok, ok, ok, F, N, F, N, U}},
		{"point a site domain", func(w *redirectWorld, a Actor) error {
			_, err := w.svc.PointDomainOf(ctx, a, "bobsite.example.com")
			return err
		}, []Kind{ok, ok, ok, F, ok, F, N, U}},
		{"create a zone", func(w *redirectWorld, a Actor) error {
			_, err := w.svc.CreateDNSZone(ctx, a, idString(w.ownConn(a)), "fresh.com")
			return err
		}, []Kind{ok, ok, ok, F, F, F, F, U}},
		{"create a zone in another's connection", func(w *redirectWorld, a Actor) error {
			_, err := w.svc.CreateDNSZone(ctx, a, idString(w.otherConn(a)), "fresh.com")
			return err
		}, []Kind{F, F, N, F, F, F, F, U}},
		{"delete a zone", func(w *redirectWorld, a Actor) error {
			return w.svc.DeleteDNSZone(ctx, a, idString(w.ownConn(a)), "spare.com", "spare.com")
		}, []Kind{ok, ok, ok, F, F, F, F, U}},
		{"delete a zone in another's connection", func(w *redirectWorld, a Actor) error {
			return w.svc.DeleteDNSZone(ctx, a, idString(w.otherConn(a)), "spare.com", "spare.com")
		}, []Kind{F, F, N, F, F, F, F, U}},
	}
	for _, op := range ops {
		for i, ac := range actors {
			t.Run(op.name+"/"+ac.name, func(t *testing.T) {
				w := newRedirectWorld(t)
				err := op.run(w, ac.a)
				if want := op.want[i]; want == ok {
					if err != nil {
						t.Fatalf("want success, got %v: %v", KindOf(err), err)
					}
				} else {
					wantKind(t, err, want)
				}
			})
		}
	}
}

func TestRedirectListsPerActor(t *testing.T) {
	w := newRedirectWorld(t)
	ctx := context.Background()
	for _, c := range []struct {
		name string
		a    Actor
		want []string
	}{
		{"admin", admin, []string{"bobold.example.com", "bobsite.example.com/old", "rootold.example.com"}},
		{"admin read token", rootRead, []string{"bobold.example.com", "bobsite.example.com/old", "rootold.example.com"}},
		{"member", bob, []string{"bobold.example.com", "bobsite.example.com/old"}},
		{"member read token", bobRead, []string{"bobold.example.com", "bobsite.example.com/old"}},
		{"member site token", bobLimited, []string{"bobsite.example.com/old"}},
		{"admin site token", rootLimited, []string{}},
	} {
		list, err := w.svc.Redirects(ctx, c.a)
		if err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, r := range list {
			got = append(got, r.SourceDomain+r.SourcePath)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	list, _ := w.svc.Redirects(ctx, admin)
	if list[1].Owner != "bob" || list[2].Owner != "root" || list[1].CreatedAt.IsZero() {
		t.Fatalf("owners: %+v", list)
	}
	if _, err := w.svc.Redirects(ctx, nobody); KindOf(err) != Unauthenticated {
		t.Fatalf("nobody: %v", err)
	}
}

func TestRedirectsReachTheWebServer(t *testing.T) {
	svc, web, _ := newService(t)
	ctx := context.Background()
	if _, err := svc.SaveSite(ctx, admin, "blog", []string{"blog.example.com"}); err != nil {
		t.Fatal(err)
	}
	r, err := svc.CreateRedirect(ctx, admin, RedirectInput{SourceDomain: "www.blog.example.com", Target: "https://blog.example.com", Code: 301, KeepPath: true})
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.CreateRedirect(ctx, admin, RedirectInput{SourceDomain: "blog.example.com", SourcePath: "/old/*", Target: "/new", Code: 308, KeepPath: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg := web.last()
	for _, want := range []string{
		`"host":["www.blog.example.com"]`,
		`https://blog.example.com{http.request.uri.path}`,
		`"pattern":"^/old/+(.*)$"`,
		`/new/{http.regexp.redirect.1}`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %s: %s", want, cfg)
		}
	}

	// The site cannot take the redirected domain, nor can the panel.
	if _, err := svc.SaveSite(ctx, admin, "blog", []string{"blog.example.com", "www.blog.example.com"}); KindOf(err) != Invalid {
		t.Fatalf("site takes a redirect domain: %v", err)
	}
	d := "www.blog.example.com"
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{PanelDomain: &d}); KindOf(err) != Conflict {
		t.Fatalf("panel takes a redirect domain: %v", err)
	}
	// A domain redirect cannot take a site's domain.
	_, err = svc.CreateRedirect(ctx, admin, RedirectInput{SourceDomain: "blog.example.com", Target: "https://example.org", Code: 301})
	if KindOf(err) != Invalid || !strings.Contains(err.Error(), "belongs to the site blog") {
		t.Fatalf("domain redirect on a site domain: %v", err)
	}
	// Twice the same.
	_, err = svc.CreateRedirect(ctx, admin, RedirectInput{SourceDomain: "www.blog.example.com", Target: "https://example.org", Code: 301})
	wantKind(t, err, Conflict)
	_, err = svc.CreateRedirect(ctx, admin, RedirectInput{SourceDomain: "blog.example.com", SourcePath: "/old/*", Target: "/x", Code: 301})
	wantKind(t, err, Conflict)
	// Path redirects need a site with the domain.
	_, err = svc.CreateRedirect(ctx, admin, RedirectInput{SourceDomain: "nosite.example.com", SourcePath: "/a", Target: "/b", Code: 301})
	wantKind(t, err, Invalid)

	// Failing web server: saved, but NotSynced.
	web.setFail(context.DeadlineExceeded)
	u, err := svc.UpdateRedirect(ctx, admin, r.ID, RedirectInput{SourceDomain: "www.blog.example.com", Target: "https://blog.example.com", Code: 302})
	wantKind(t, err, NotSynced)
	if u.Code != 302 {
		t.Fatalf("not returned with NotSynced: %+v", u)
	}
	web.setFail(nil)

	// Deleting the site removes its path redirects.
	if err := svc.DeleteSite(ctx, admin, "blog"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Redirect(ctx, admin, p.ID); KindOf(err) != NotFound {
		t.Fatalf("path redirect after site delete: %v", err)
	}
	list, _ := svc.Redirects(ctx, admin)
	if len(list) != 1 || list[0].ID != r.ID {
		t.Fatalf("after site delete: %+v", list)
	}
	if strings.Contains(web.last(), "/old/") {
		t.Fatalf("stale path redirect in the config: %s", web.last())
	}
	if err := svc.DeleteRedirect(ctx, admin, r.ID); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(web.last(), "www.blog.example.com") {
		t.Fatalf("deleted domain redirect in the config")
	}

	acts, _ := svc.Activities(ctx, admin, 50)
	var actions []string
	for _, a := range acts {
		if strings.HasPrefix(a.Action, "redirect.") {
			actions = append(actions, a.Action+" "+a.Target)
		}
	}
	want := []string{
		"redirect.delete www.blog.example.com",
		"redirect.update www.blog.example.com",
		"redirect.create blog.example.com/old/*",
		"redirect.create www.blog.example.com",
	}
	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("activity %v", actions)
	}
}

func TestRedirectLimitPerUser(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	for i := 0; i < MaxRedirectsPerUser; i++ {
		if _, err := svc.store.CreateRedirect(ctx, storeRedirect(i), 0); err != nil {
			t.Fatal(err)
		}
	}
	_, err := svc.CreateRedirect(ctx, admin, RedirectInput{SourceDomain: "one-more.example.com", Target: "https://example.com", Code: 301})
	if KindOf(err) != Conflict || !strings.Contains(err.Error(), "at most 500") {
		t.Fatalf("over the limit: %v", err)
	}
}

func TestRedirectJSONShape(t *testing.T) {
	svc, _, _ := newService(t)
	r, err := svc.CreateRedirect(context.Background(), admin, RedirectInput{SourceDomain: "old.example.com", Target: "https://example.com", Code: 307, KeepQuery: true})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r)
	var m map[string]any
	json.Unmarshal(b, &m)
	keys := []string{}
	for k := range m {
		keys = append(keys, k)
	}
	if len(keys) != 9 || m["owner"] != "root" || m["source_path"] != "" || m["code"] != float64(307) || m["keep_query"] != true {
		t.Fatalf("%s", b)
	}
}

func TestDomainStatusOfRedirectDomain(t *testing.T) {
	w := newRedirectWorld(t)
	ctx := context.Background()
	w.res.answers["bobold.example.com"] = []netip.Addr{serverV4}
	st, err := w.svc.DomainStatusOf(ctx, bob, "BobOld.example.com.")
	if err != nil {
		t.Fatal(err)
	}
	if st.Domain != "bobold.example.com" || st.State != DomainPointsHere || st.Zone == nil || st.Zone.ConnectionName != "bob-main" || st.Zone.RecordName != "bobold" {
		t.Fatalf("status %+v", st)
	}
	res, err := w.svc.PointDomainOf(ctx, bob, "bobold.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changed) != 2 {
		t.Fatalf("changed %+v", res.Changed)
	}
	if rs, ok := w.hz[bobToken].RRSet("example.com", "bobold", "A"); !ok || rs.Records[0] != serverV4.String() {
		t.Fatalf("record %+v %v", rs, ok)
	}
	// The site-scoped call gives the same object.
	list, err := w.svc.DomainStatuses(ctx, bob, "bobsite")
	if err != nil {
		t.Fatal(err)
	}
	one, err := w.svc.DomainStatusOf(ctx, bob, "bobsite.example.com")
	if err != nil || !reflect.DeepEqual(list[0], one) {
		t.Fatalf("site status %+v vs %+v (%v)", list[0], one, err)
	}
}

func TestDNSZoneCreateAndDelete(t *testing.T) {
	w := newRedirectWorld(t)
	ctx := context.Background()
	srv := w.hz[bobToken]

	z, err := w.svc.CreateDNSZone(ctx, bob, idString(w.bobConn), " Fresh.COM ")
	if err != nil {
		t.Fatal(err)
	}
	want := DNSZone{ConnectionID: idString(w.bobConn), ConnectionName: "bob-main", Name: "fresh.com",
		Nameservers: []string{"hydrogen.ns.hetzner.com", "oxygen.ns.hetzner.com", "helium.ns.hetzner.de"},
		Adopted:     true, UsedBy: []string{}}
	if !reflect.DeepEqual(z, want) {
		t.Fatalf("created %+v", z)
	}
	if !srv.HasZone("fresh.com") {
		t.Fatal("not created at Hetzner")
	}
	zones, err := w.svc.DNSZones(ctx, bob)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range zones.Zones {
		found = found || (x.Name == "fresh.com" && reflect.DeepEqual(x.Nameservers, want.Nameservers))
	}
	if !found {
		t.Fatalf("new zone not listed with nameservers: %+v", zones)
	}

	for name, kind := range map[string]Kind{
		"fresh.com":     Conflict, // already there
		"www.fresh.com": Invalid,
		"com":           Invalid,
		"co.uk":         Invalid,
		"203.0.113.5":   Invalid,
		"bad name.com":  Invalid,
		"münchen.de":    Invalid,
		"":              Invalid,
	} {
		_, err := w.svc.CreateDNSZone(ctx, bob, idString(w.bobConn), name)
		if KindOf(err) != kind {
			t.Errorf("%q: %v, want %v", name, err, kind)
		}
	}
	// Exists elsewhere at Hetzner.
	w.svc.dnsCache.dropZones(w.bobConn)
	srv.Fail("POST /zones", 1, hetznerUniqueness)
	if _, err := w.svc.CreateDNSZone(ctx, bob, idString(w.bobConn), "taken.com"); KindOf(err) != Conflict || !strings.Contains(err.Error(), "already exists at Hetzner") {
		t.Fatalf("uniqueness: %v", err)
	}

	// Delete: confirmation, use, protection.
	if err := w.svc.DeleteDNSZone(ctx, bob, idString(w.bobConn), "fresh.com", "fresh.co"); KindOf(err) != Invalid {
		t.Fatalf("wrong confirmation: %v", err)
	}
	if err := w.svc.DeleteDNSZone(ctx, bob, idString(w.bobConn), "nope.com", "nope.com"); KindOf(err) != NotFound {
		t.Fatalf("unknown zone: %v", err)
	}
	err = w.svc.DeleteDNSZone(ctx, bob, idString(w.bobConn), "example.com", "example.com")
	if KindOf(err) != Conflict {
		t.Fatalf("zone in use: %v", err)
	}
	for _, want := range []string{"the site bobsite (bobsite.example.com)", "the site bobother (bobother.example.com)",
		"a site of another person (rootsite.example.com)", "the redirect of bobold.example.com",
		"a redirect of another person (rootold.example.com)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("in-use message lacks %q: %v", want, err)
		}
	}
	// The panel domain counts too.
	w.svc.settings.panelDomain = "panel.fresh.com"
	if err := w.svc.DeleteDNSZone(ctx, bob, idString(w.bobConn), "fresh.com", "fresh.com"); KindOf(err) != Conflict || !strings.Contains(err.Error(), "the panel itself (panel.fresh.com)") {
		t.Fatalf("panel domain: %v", err)
	}
	w.svc.settings.panelDomain = ""
	srv.ProtectZone("fresh.com", true)
	if err := w.svc.DeleteDNSZone(ctx, bob, idString(w.bobConn), "fresh.com", "fresh.com"); KindOf(err) != Conflict || !strings.Contains(err.Error(), "protected against deletion") {
		t.Fatalf("protected: %v", err)
	}
	srv.ProtectZone("fresh.com", false)
	if err := w.svc.DeleteDNSZone(ctx, bob, idString(w.bobConn), "fresh.com", "fresh.com."); err != nil {
		t.Fatal(err)
	}
	if srv.HasZone("fresh.com") {
		t.Fatal("still at Hetzner")
	}
	acts, _ := w.svc.Activities(ctx, admin, 50)
	var actions []string
	for _, a := range acts {
		if strings.HasPrefix(a.Action, "dns.zone.") {
			actions = append(actions, a.Action+" "+a.Target)
		}
	}
	if !reflect.DeepEqual(actions, []string{"dns.zone.delete fresh.com", "dns.zone.create fresh.com"}) {
		t.Fatalf("activity %v", actions)
	}
}
