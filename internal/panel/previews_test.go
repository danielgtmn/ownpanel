package panel

import (
	"context"
	"net/netip"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/caddy"
)

// lastConfig is the last config loaded into the web server.
func lastConfig(t *testing.T, web *fakeWeb) string {
	t.Helper()
	return web.last()
}

func TestPublishWithoutMakingLive(t *testing.T) {
	ctx := context.Background()
	svc, web, _ := newService(t)
	if _, err := svc.SaveSite(ctx, admin, "a", []string{"a.example.com"}); err != nil {
		t.Fatal(err)
	}

	// Without a preview domain: publishing works, there is no address.
	p, err := svc.PublishVersion(ctx, admin, "a", tarGz(t, map[string]string{"index.html": "first"}), false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Current != "" || p.Live || p.Release == "" || p.PreviewURL != "" || p.PreviewAddresses {
		t.Fatalf("%+v", p)
	}
	if len(p.Versions) != 1 || p.Versions[0].ID != p.Release || p.Versions[0].Live || !p.Versions[0].Pending {
		t.Fatalf("versions %+v", p.Versions)
	}
	// The site still shows that it has no content.
	if err := svc.Sync(ctx, System); err != nil {
		t.Fatal(err)
	}
	if cfg := lastConfig(t, web); !strings.Contains(cfg, "no content yet") || strings.Contains(cfg, "file_server") {
		t.Fatalf("site without a live version is served: %s", cfg)
	}

	// Making it live serves it (the config changes for the first one).
	loads := web.count()
	site, err := svc.MakeLive(ctx, admin, "a", p.Release)
	if err != nil {
		t.Fatal(err)
	}
	if site.Current != p.Release || site.Versions[0].Pending || !site.Versions[0].Live {
		t.Fatalf("%+v", site)
	}
	if web.count() == loads || !strings.Contains(lastConfig(t, web), "file_server") {
		t.Fatal("making the first version live did not update the web server")
	}

	// A second one does not change what is live.
	p2, err := svc.PublishVersion(ctx, admin, "a", tarGz(t, map[string]string{"index.html": "second"}), false)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Current != p.Release || p2.Versions[0].ID != p2.Release || !p2.Versions[0].Pending || !p2.Versions[1].Live {
		t.Fatalf("%+v", p2)
	}
	acts, _ := svc.store.Activities(ctx, 1)
	if acts[0].Action != "site.publish-not-live" || acts[0].Detail != p2.Release {
		t.Fatalf("activity %+v", acts[0])
	}
	// Publish keeps making versions live.
	p3, err := svc.Publish(ctx, admin, "a", tarGz(t, map[string]string{"index.html": "third"}))
	if err != nil || p3.Current == p.Release || p3.Current == p2.Release {
		t.Fatalf("%+v %v", p3, err)
	}
}

func TestPreviewAddresses(t *testing.T) {
	ctx := context.Background()
	svc, web, _ := newService(t)
	svc.opts.PanelUpstream = "127.0.0.1:8600"
	svc.opts.TLSMode = caddy.TLSInternal
	if _, err := svc.SaveSite(ctx, admin, "a", []string{"a.example.com"}); err != nil {
		t.Fatal(err)
	}
	live, err := svc.Publish(ctx, admin, "a", tarGz(t, map[string]string{"index.html": "live"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{PreviewDomain: str("Preview.Example.NET")}); err != nil {
		t.Fatal(err)
	}
	if svc.PreviewDomain() != "preview.example.net" {
		t.Fatalf("stored %q", svc.PreviewDomain())
	}
	p, err := svc.PublishVersion(ctx, admin, "a", tarGz(t, map[string]string{"index.html": "new"}), false)
	if err != nil {
		t.Fatal(err)
	}
	if !p.PreviewAddresses || p.PreviewURL == "" || p.PreviewURL != p.Versions[0].PreviewURL || p.Versions[1].PreviewURL == "" {
		t.Fatalf("%+v", p)
	}
	u, err := url.Parse(p.PreviewURL)
	if err != nil {
		t.Fatal(err)
	}
	label, ok := strings.CutSuffix(u.Hostname(), ".preview.example.net")
	if !ok || !regexp.MustCompile(`^[a-z2-7]{26}$`).MatchString(label) || u.Port() != "8443" || u.Scheme != "https" {
		t.Fatalf("address %s", p.PreviewURL)
	}
	key := u.Query().Get(caddy.PreviewQuery)
	if len(key) != 32 || u.Path != "/" {
		t.Fatalf("key %q in %s", key, p.PreviewURL)
	}
	// The address does not reveal the site or the version, and differs per
	// version and per site.
	if strings.Contains(p.PreviewURL, "a.example") || strings.Contains(strings.ToLower(p.PreviewURL), strings.ToLower(p.Release[:8])) {
		t.Fatalf("address reveals the version: %s", p.PreviewURL)
	}
	if svc.previewLabel("a", p.Release) == svc.previewLabel("a", live.Current) || svc.previewLabel("a", p.Release) == svc.previewLabel("b", p.Release) {
		t.Fatal("labels collide")
	}

	// The config serves every kept version below the preview domain, and
	// the live site is unchanged.
	cfg := lastConfig(t, web)
	for _, want := range []string{label + ".preview.example.net", svc.deploys.ReleaseDir("a", p.Release), svc.deploys.ReleaseDir("a", live.Current), key,
		`"endpoint":"http://127.0.0.1:8600` + PreviewPermissionPath + `"`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config misses %s", want)
		}
	}

	// The web server may obtain certificates for exactly these names.
	for host, want := range map[string]bool{
		label + ".preview.example.net":                               true,
		strings.ToUpper(label) + ".preview.example.net.":             true,
		svc.previewLabel("a", live.Current) + ".preview.example.net": true,
		"preview.example.net":                                        false,
		"x." + label + ".preview.example.net":                        false,
		label + ".example.net":                                       false,
		svc.previewLabel("a", "nope") + ".preview.example.net":       false,
		strings.Repeat("a", 26) + ".preview.example.net":             false,
		"a.example.com":                                              false,
	} {
		if got, err := svc.PreviewCertificateAllowed(ctx, host); err != nil || got != want {
			t.Errorf("%s: %v %v, want %v", host, got, err, want)
		}
	}

	// Addresses are stable across restarts (the secret is stored), and
	// changing the preview domain keeps the labels.
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{PreviewDomain: str("pv.example.org")}); err != nil {
		t.Fatal(err)
	}
	site, _ := svc.Site(ctx, admin, "a")
	if !strings.Contains(site.Versions[0].PreviewURL, label+".pv.example.org") {
		t.Fatalf("after changing the domain: %s", site.Versions[0].PreviewURL)
	}
	if ok, _ := svc.PreviewCertificateAllowed(ctx, label+".preview.example.net"); ok {
		t.Fatal("old domain still allowed")
	}

	// Removed versions lose their address.
	if err := svc.DeleteSite(ctx, admin, "a"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := svc.PreviewCertificateAllowed(ctx, label+".pv.example.org"); ok {
		t.Fatal("preview of a deleted site still allowed")
	}

	// Turned off: no addresses, nothing in the config.
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{PreviewDomain: str("")}); err != nil {
		t.Fatal(err)
	}
	if cfg := lastConfig(t, web); strings.Contains(cfg, "on_demand") || strings.Contains(cfg, "pv.example.org") {
		t.Fatalf("previews still configured: %s", cfg)
	}
}

func TestPreviewSecretSurvivesRestart(t *testing.T) {
	d := newDNSWorld(t) // has a key box
	ctx := context.Background()
	if err := d.svc.initPreviewSecret(ctx); err != nil {
		t.Fatal(err)
	}
	first := d.svc.previewLabel("a", "r")
	d.svc.previewSecret = nil
	if err := d.svc.initPreviewSecret(ctx); err != nil {
		t.Fatal(err)
	}
	if d.svc.previewLabel("a", "r") != first {
		t.Fatal("the preview secret changed on restart")
	}
	m, _ := d.svc.store.Settings(ctx)
	if v := m[keyPreviewSecret]; v == "" || strings.Contains(v, string(d.svc.previewSecret)) {
		t.Fatal("secret not stored sealed")
	}
}

func TestPreviewDomainRules(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newService(t)
	svc.opts.PanelUpstream = "127.0.0.1:8600"
	if _, err := svc.SaveSite(ctx, admin, "a", []string{"www.example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{PanelDomain: str("panel.example.org")}); err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]Kind{
		"203.0.113.5":         Invalid,
		"not a domain":        Invalid,
		"example":             Invalid,
		"*.preview.example":   Invalid,
		"example.com":         Conflict, // www.example.com is a site
		"example.org":         Conflict, // the panel is below it
		"preview.example.com": ok,
		"example.org.":        Conflict,
	} {
		_, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{PreviewDomain: str(in)})
		if got := kindOrOK(err); got != want {
			t.Errorf("%q: %v, want %v", in, err, want)
		}
	}
	// Names below it are taken from now on.
	if _, err := svc.SaveSite(ctx, admin, "b", []string{"x.preview.example.com"}); KindOf(err) != Invalid {
		t.Errorf("site below the preview domain: %v", err)
	}
	if _, err := svc.SaveSite(ctx, admin, "b", []string{"preview.example.com"}); err != nil {
		t.Errorf("the preview domain itself may be a site: %v", err)
	}
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{PanelDomain: str("p.preview.example.com")}); KindOf(err) != Invalid {
		t.Errorf("panel below the preview domain: %v", err)
	}
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailHost: str("mail.preview.example.com")}); KindOf(err) != Invalid {
		t.Errorf("mail host below the preview domain: %v", err)
	}
	if _, err := svc.CreateRedirect(ctx, admin, RedirectInput{SourceDomain: "r.preview.example.com", Target: "https://example.net", Code: 301}); KindOf(err) != Invalid {
		t.Errorf("redirect below the preview domain: %v", err)
	}
}

func kindOrOK(err error) Kind {
	if err == nil {
		return ok
	}
	return KindOf(err)
}

func TestPreviewPermissions(t *testing.T) {
	ctx := context.Background()
	actors := []struct {
		name string
		a    Actor
	}{
		{"admin", admin}, {"admin write token", rootWriteTok}, {"member", bob}, {"member read token", bobRead},
		{"member site token", bobLimited}, {"admin read token", rootRead}, {"admin site token", rootLimited},
		{"nobody", nobody},
	}
	F, N, U := Forbidden, NotFound, Unauthenticated
	ops := []struct {
		name string
		run  func(w world, a Actor) error
		want []Kind
	}{
		{"publish without making live to own site", func(w world, a Actor) error {
			_, err := w.svc.PublishVersion(ctx, a, "bobsite", tarGz(t, map[string]string{"index.html": "x"}), false)
			return err
		}, []Kind{ok, ok, ok, F, ok, F, N, U}},
		{"publish without making live to member's other site", func(w world, a Actor) error {
			_, err := w.svc.PublishVersion(ctx, a, "bobother", tarGz(t, map[string]string{"index.html": "x"}), false)
			return err
		}, []Kind{ok, ok, ok, F, N, F, N, U}},
		{"publish without making live to admin's site", func(w world, a Actor) error {
			_, err := w.svc.PublishVersion(ctx, a, "rootsite", tarGz(t, map[string]string{"index.html": "x"}), false)
			return err
		}, []Kind{ok, ok, N, N, N, F, ok, U}},
		{"make a version that is not live live", func(w world, a Actor) error {
			p, err := w.svc.PublishVersion(ctx, System, "bobsite", tarGz(t, map[string]string{"index.html": "x"}), false)
			if err != nil {
				t.Fatal(err)
			}
			_, err = w.svc.MakeLive(ctx, a, "bobsite", p.Release)
			return err
		}, []Kind{ok, ok, ok, F, ok, F, N, U}},
		{"set the preview domain", func(w world, a Actor) error {
			_, err := w.svc.UpdateSettings(ctx, a, SettingsUpdate{PreviewDomain: str("preview.example.net")})
			return err
		}, []Kind{ok, ok, F, F, F, F, F, U}},
		{"point the preview domain here", func(w world, a Actor) error {
			_, err := w.svc.PointPreviewDomain(ctx, a)
			if KindOf(err) == Conflict { // allowed, but no domain set
				return nil
			}
			return err
		}, []Kind{ok, ok, F, F, F, F, F, U}},
	}
	for _, op := range ops {
		for i, ac := range actors {
			t.Run(op.name+"/"+ac.name, func(t *testing.T) {
				w := newWorld(t)
				if got := kindOrOK(op.run(w, ac.a)); got != op.want[i] {
					t.Fatalf("got %v, want %v", got, op.want[i])
				}
			})
		}
	}
	// A site someone may not see shows nothing of its previews.
	w := newWorld(t)
	w.svc.opts.PanelUpstream = "127.0.0.1:8600"
	if _, err := w.svc.UpdateSettings(ctx, admin, SettingsUpdate{PreviewDomain: str("preview.example.net")}); err != nil {
		t.Fatal(err)
	}
	sites, err := w.svc.Sites(ctx, bob)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sites {
		if s.Name == "rootsite" {
			t.Fatal("member sees admin's site")
		}
		if len(s.Versions) > 0 && s.Versions[0].PreviewURL == "" {
			t.Fatalf("no preview address for %s", s.Name)
		}
	}
}

func TestPointPreviewDomain(t *testing.T) {
	ctx := context.Background()
	d := newDNSWorld(t)
	d.svc.opts.PanelUpstream = "127.0.0.1:8600"
	if _, err := d.svc.UpdateSettings(ctx, admin, SettingsUpdate{PreviewDomain: str("preview.example.com")}); err != nil {
		t.Fatal(err)
	}
	checks, err := d.svc.SettingsChecks(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	pd := checks.PreviewDNS
	if pd.Name != "*.preview.example.com" || pd.State != DomainNoRecord || pd.Zone == nil || pd.Zone.Zone != "example.com" || pd.Zone.RecordName != "*.preview" {
		t.Fatalf("before: %+v %+v", pd, pd.Zone)
	}
	res, err := d.svc.PointPreviewDomain(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changed) != 2 || res.Changed[0].Name != "*.preview" {
		t.Fatalf("%+v", res.Changed)
	}
	a, _ := d.hz[rootToken].RRSet("example.com", "*.preview", "A")
	if !reflect.DeepEqual(a.Records, []string{"203.0.113.5"}) {
		t.Fatalf("stored %+v", a)
	}
	d.res.answers[previewCheckLabel+".preview.example.com"] = []netip.Addr{serverV4, serverV6}
	checks, _ = d.svc.SettingsChecks(ctx, admin)
	if checks.PreviewDNS.State != DomainPointsHere {
		t.Fatalf("after: %+v", checks.PreviewDNS)
	}
	// A domain in no zone of the admin: records to create, nothing to offer.
	if _, err := d.svc.UpdateSettings(ctx, admin, SettingsUpdate{PreviewDomain: str("preview.elsewhere.example")}); err != nil {
		t.Fatal(err)
	}
	checks, _ = d.svc.SettingsChecks(ctx, admin)
	if checks.PreviewDNS.Zone != nil || checks.PreviewDNS.Name != "*.preview.elsewhere.example" || len(checks.PreviewDNS.Expected) != 2 {
		t.Fatalf("elsewhere: %+v", checks.PreviewDNS)
	}
	if _, err := d.svc.PointPreviewDomain(ctx, admin); KindOf(err) != Conflict {
		t.Fatalf("pointing without a zone: %v", err)
	}
}
