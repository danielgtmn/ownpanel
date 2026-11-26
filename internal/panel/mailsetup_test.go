package panel

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/store"
)

func TestMergeHostSPF(t *testing.T) {
	for _, c := range []struct{ in, want, conflict string }{
		{"v=spf1 -all", "v=spf1 a -all", ""},
		{"v=spf1 ip4:192.0.2.1 mx ~all", "v=spf1 a ip4:192.0.2.1 mx ~all", ""},
		{"v=spf1 a -all", "v=spf1 a -all", ""},
		{"v=spf1 -a -all", "", "the mail server itself"},
		{"v=spf1 a ?all", "", "?all"},
	} {
		m := mergeSPF(c.in, "a")
		if c.conflict != "" {
			if !strings.Contains(m.conflict, c.conflict) {
				t.Errorf("%q: want conflict %q, got %+v", c.in, c.conflict, m)
			}
			continue
		}
		if m.conflict != "" || m.value != c.want || m.ok != (c.in == c.want) {
			t.Errorf("%q: %+v, want %q", c.in, m, c.want)
		}
	}
	// mx in a mail host's record counts as a lookup, not as a.
	if m := mergeSPF("v=spf1 mx -all", "a"); m.ok || m.value != "v=spf1 a mx -all" {
		t.Fatalf("%+v", m)
	}
}

// TestMailHostSPF: mail health checks the SPF record of the mail host;
// admins whose DNS connection holds its zone set it up.
func TestMailHostSPF(t *testing.T) {
	w := newRecordsWorld(t)
	ctx := context.Background()
	svc := w.svc
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailHost: strp("mail.root.org")}); err != nil {
		t.Fatal(err)
	}
	w.rootDNS.put("root.org", "mail", "TXT", "other=1", "v=spf1 ip4:192.0.2.1 -all")

	h, err := svc.MailHealth(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	c := healthCheck(t, h, HealthMailHostSPF)
	if c.State != HealthWarning || !c.CanApply || !slices.Equal(c.Expected, []string{"v=spf1 a ip4:192.0.2.1 -all"}) ||
		!slices.Equal(c.Found, []string{"v=spf1 ip4:192.0.2.1 -all"}) {
		t.Fatalf("before %+v", c)
	}
	// A read-only admin token sees the check, but cannot apply.
	if h, err := svc.MailHealth(ctx, rootRead); err != nil || healthCheck(t, h, HealthMailHostSPF).CanApply {
		t.Fatalf("read token %+v %v", h, err)
	}

	for _, a := range []Actor{bob, rootRead, bobLimited, System} {
		_, err := svc.SetUpMailHostSPF(ctx, a)
		wantKind(t, err, Forbidden)
	}
	_, err = svc.SetUpMailHostSPF(ctx, nobody)
	wantKind(t, err, Unauthenticated)

	res, err := svc.SetUpMailHostSPF(ctx, admin)
	if err != nil || !res.Applied || len(res.Changes) != 1 || res.Changes[0].Action != ChangeReplace {
		t.Fatalf("%+v %v", res, err)
	}
	if v := w.rootDNS.get("root.org", "mail", "TXT"); !slices.Equal(v, []string{"other=1", "v=spf1 a ip4:192.0.2.1 -all"}) {
		t.Fatalf("TXT %v", v)
	}
	h, _ = svc.MailHealth(ctx, admin)
	if c := healthCheck(t, h, HealthMailHostSPF); c.State != HealthOK || c.CanApply {
		t.Fatalf("after %+v", c)
	}
	acts, _ := svc.store.Activities(ctx, 5)
	if !slices.ContainsFunc(acts, func(a store.Activity) bool { return a.Action == "mail.host-spf.set" && a.Target == "mail.root.org" }) {
		t.Fatalf("activity %+v", acts)
	}

	// A mail host outside the admin's zones: nothing to apply.
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{MailHost: strp("mail.elsewhere.net")}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.SetUpMailHostSPF(ctx, admin)
	wantKind(t, err, Conflict)
	h, _ = svc.MailHealth(ctx, admin)
	if c := healthCheck(t, h, HealthMailHostSPF); c.State != HealthWarning || c.CanApply || !slices.Equal(c.Expected, []string{defaultHostSPF}) {
		t.Fatalf("elsewhere %+v", c)
	}
}

func TestMailSetupRecords(t *testing.T) {
	w := newRecordsWorld(t)
	ctx := context.Background()
	b := w.bobDNS
	b.put("bobzone.org", "autoconfig", "A", serverV4.String())
	b.put("bobzone.org", "autodiscover", "A", "198.51.100.7")
	b.put("fresh.org", "autoconfig", "CNAME", "mail.other.net.")
	b.put("fresh.org", "autodiscover", "TXT", "hello")

	r, err := w.svc.MailRecords(ctx, bob, "bobzone.org")
	if err != nil {
		t.Fatal(err)
	}
	ac, ad := recordOf(t, r, MailRecordAutoconfig), recordOf(t, r, MailRecordAutodiscover)
	if ac.Status != RecordOK || !ac.Optional || ac.Type != "CNAME" || ac.Want != testMailHost+"." || ac.FQDN != "autoconfig.bobzone.org" {
		t.Fatalf("autoconfig %+v", ac)
	}
	if ad.Status != RecordDifferent || !slices.Equal(ad.Found, []string{"198.51.100.7"}) || !ad.Optional {
		t.Fatalf("autodiscover %+v", ad)
	}
	if mx := recordOf(t, r, MailRecordMX); mx.Optional {
		t.Fatal("mx is not optional")
	}

	res, err := w.svc.SetUpMailRecords(ctx, bob, "bobzone.org", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Changes {
		switch c.Kind {
		case MailRecordAutoconfig:
			if c.Action != ChangeKeep || !c.Optional {
				t.Fatalf("autoconfig %+v", c)
			}
		case MailRecordAutodiscover:
			if c.Action != ChangeSkip || !strings.Contains(c.Note, "another server") {
				t.Fatalf("autodiscover %+v", c)
			}
		}
	}
	if v := b.get("bobzone.org", "autodiscover", "A"); !slices.Equal(v, []string{"198.51.100.7"}) {
		t.Fatalf("address records changed: %v", v)
	}
	if v := b.get("bobzone.org", "autodiscover", "CNAME"); v != nil {
		t.Fatalf("CNAME next to A: %v", v)
	}

	res, err = w.svc.SetUpMailRecords(ctx, bob, "fresh.org", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Changes {
		switch c.Kind {
		case MailRecordAutoconfig:
			if c.Action != ChangeReplace || !slices.Equal(c.Old, []string{"mail.other.net."}) {
				t.Fatalf("autoconfig %+v", c)
			}
		case MailRecordAutodiscover:
			if c.Action != ChangeSkip || !strings.Contains(c.Note, "other records") {
				t.Fatalf("autodiscover %+v", c)
			}
		}
	}
	if v := b.get("fresh.org", "autoconfig", "CNAME"); !slices.Equal(v, []string{testMailHost + "."}) {
		t.Fatalf("CNAME %v", v)
	}
	if v := b.get("fresh.org", "autodiscover", "TXT"); !slices.Equal(v, []string{"hello"}) {
		t.Fatalf("TXT changed: %v", v)
	}
}

// TestMailSetupHosts: the web server serves the names for setting up mail
// programs only once they point here, and the well-known path on sites
// whose domain is an active mail domain.
func TestMailSetupHosts(t *testing.T) {
	w := newRecordsWorld(t)
	ctx := context.Background()
	svc := w.svc
	svc.opts.PanelUpstream = "127.0.0.1:8600"
	web := svc.web.(*fakeWeb)

	// fresh.org becomes active with its records; its CNAMEs point here.
	if _, err := svc.SetUpMailRecords(ctx, bob, "fresh.org", true); err != nil {
		t.Fatal(err)
	}
	// bobzone.org is not active: its names are never served, even when
	// they point here.
	w.bobDNS.put("bobzone.org", "autoconfig", "CNAME", testMailHost+".")
	if !svc.checkMailSetupHosts(ctx) {
		t.Fatal("no change noticed")
	}
	if err := svc.sync(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := web.last()
	for _, want := range []string{`"autoconfig.fresh.org"`, `"autodiscover.fresh.org"`, `"/autodiscover/autodiscover.xml"`} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("%s missing in %s", want, cfg)
		}
	}
	if strings.Contains(cfg, "autoconfig.bobzone.org") {
		t.Fatal("an inactive domain's name is served")
	}
	// bobsite.example.com is a site and an active mail domain: the
	// well-known path goes to the panel before the site's files.
	if !strings.Contains(cfg, `{"handle":[{"handler":"reverse_proxy"`) || !strings.Contains(cfg, `"host":["bobsite.example.com"],"path":["/.well-known/autoconfig/mail/config-v1.1.xml"]`) {
		t.Fatalf("no well-known route in %s", cfg)
	}

	// Not looked at again before mailCheckEvery, unless asked to.
	if svc.checkMailSetupHosts(ctx) {
		t.Fatal("looked again too soon")
	}
	// The name now points elsewhere: it is no longer served.
	w.bobDNS.put("fresh.org", "autodiscover", "CNAME", "mail.other.net.")
	w.look.fakeResolver.mu.Lock()
	w.look.fakeResolver.answers["mail.other.net"] = []netip.Addr{netip.MustParseAddr("198.51.100.7")}
	w.look.fakeResolver.mu.Unlock()
	svc.setupHosts.checkSoon()
	if !svc.checkMailSetupHosts(ctx) {
		t.Fatal("no change noticed")
	}
	_ = svc.sync(ctx)
	if cfg := web.last(); strings.Contains(cfg, "autodiscover.fresh.org") || !strings.Contains(cfg, "autoconfig.fresh.org") {
		t.Fatalf("after the change %s", cfg)
	}
	// A failed lookup changes nothing.
	w.look.fail("autoconfig.fresh.org", context.DeadlineExceeded)
	w.look.fakeResolver.mu.Lock()
	w.look.fakeResolver.errs["autoconfig.fresh.org"] = context.DeadlineExceeded
	w.look.fakeResolver.mu.Unlock()
	svc.setupHosts.checkSoon()
	if svc.checkMailSetupHosts(ctx) || !svc.setupHosts.pointsHere("autoconfig.fresh.org") {
		t.Fatal("a failed lookup changed the state")
	}
	// Deleting the mail domain stops serving its names.
	if err := svc.DeleteMailDomain(ctx, bob, "fresh.org", "fresh.org"); err != nil && KindOf(err) != NotSynced {
		t.Fatal(err)
	}
	svc.setupHosts.checkSoon()
	if !svc.checkMailSetupHosts(ctx) || svc.setupHosts.pointsHere("autoconfig.fresh.org") {
		t.Fatal("names of a deleted domain still served")
	}
}

func TestMailSetupFor(t *testing.T) {
	w := newRecordsWorld(t)
	ctx := context.Background()
	set, err := w.svc.MailSetupFor(ctx, "BobSite.Example.com.")
	if err != nil || set.Domain != "bobsite.example.com" || set.Client.Server != testMailHost || set.Client.IMAPPort != 993 ||
		set.Client.SMTPPort != 465 || set.Client.SMTPStartTLS != 587 {
		t.Fatalf("%+v %v", set, err)
	}
	// Inactive, unknown, the mail host itself, nonsense: all the same.
	for _, d := range []string{"bobzone.org", "unknown.example", testMailHost, "", "a b", "x"} {
		_, err := w.svc.MailSetupFor(ctx, d)
		wantKind(t, err, NotFound)
		if err.Error() != errNoMailSetup.Error() {
			t.Fatalf("%q: %v", d, err)
		}
	}
}

func TestMailboxSetupOf(t *testing.T) {
	w := newMailWorld(t)
	ctx := context.Background()
	for _, a := range []Actor{bob, bobRead, admin} {
		s, err := w.svc.MailboxSetupOf(ctx, a, idString(w.bobBox))
		if err != nil || s.Address != "info@bobsite.example.com" || s.Client.Server != testMailHost {
			t.Fatalf("%s: %+v %v", a.Name, s, err)
		}
	}
	_, err := w.svc.MailboxSetupOf(ctx, bob, idString(w.rootBox))
	wantKind(t, err, NotFound)
	_, err = w.svc.MailboxSetupOf(ctx, bobLimited, idString(w.bobBox))
	wantKind(t, err, Forbidden)
	_, err = w.svc.MailboxSetupOf(ctx, nobody, idString(w.bobBox))
	wantKind(t, err, Unauthenticated)
}
