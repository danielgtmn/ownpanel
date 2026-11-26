package panel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/store"
)

// firstRouteFor returns the first route of a loaded web server config that
// matches host, as JSON.
func firstRouteFor(t *testing.T, cfgJSON, host string) string {
	t.Helper()
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []json.RawMessage `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
		t.Fatal(err)
	}
	for _, raw := range cfg.Apps.HTTP.Servers["sites"].Routes {
		var r struct {
			Match []struct {
				Host []string `json:"host"`
			} `json:"match"`
		}
		_ = json.Unmarshal(raw, &r)
		if len(r.Match) > 0 && slices.Contains(r.Match[0].Host, host) {
			return string(raw)
		}
	}
	t.Fatalf("no route for %s in %s", host, cfgJSON)
	return ""
}

func isSetupRoute(route string) bool {
	return strings.Contains(route, `"subroute"`) && strings.Contains(route, `/mail/config-v1.1.xml`) && !strings.Contains(route, `"file_server"`)
}

// A member cannot claim another member's mail domain or its names for
// setting up mail programs as a site or domain redirect; the panel keeps
// serving the setup names even when somebody else holds them.
func TestOthersMailSetupNamesStayWithTheOwner(t *testing.T) {
	w := newRecordsWorld(t)
	ctx := context.Background()
	svc := w.svc
	svc.opts.PanelUpstream = "127.0.0.1:8600"
	web := svc.web.(*fakeWeb)
	carol := Actor{UserID: addUser(t, svc, "carol", store.RoleMember).ID, Name: "carol", Via: ViaSession}

	if _, err := svc.SetUpMailRecords(ctx, bob, "fresh.org", true); err != nil {
		t.Fatal(err)
	}
	if !svc.checkMailSetupHosts(ctx) {
		t.Fatal("no change noticed")
	}

	// carol: neither as a site nor as a redirect, on create or update.
	for _, d := range []string{"autoconfig.fresh.org", "autodiscover.fresh.org", "fresh.org"} {
		_, err := svc.SaveSite(ctx, carol, "carolsite", []string{d})
		wantKind(t, err, Invalid)
		_, err = svc.CreateRedirect(ctx, carol, RedirectInput{SourceDomain: d, Target: "https://attacker.test", Code: 302})
		wantKind(t, err, Invalid)
	}
	if _, err := svc.SaveSite(ctx, carol, "carolsite", []string{"carol.example.org"}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.SaveSite(ctx, carol, "carolsite", []string{"carol.example.org", "autoconfig.fresh.org"})
	wantKind(t, err, Invalid)
	r, err := svc.CreateRedirect(ctx, carol, RedirectInput{SourceDomain: "go.carol.example.org", Target: "https://attacker.test", Code: 302})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.UpdateRedirect(ctx, carol, r.ID, RedirectInput{SourceDomain: "autodiscover.fresh.org", Target: "https://attacker.test", Code: 302})
	wantKind(t, err, Invalid)

	// The owner and admins may.
	if _, err := svc.SaveSite(ctx, bob, "bobsetup", []string{"autoconfig.fresh.org"}); err != nil && KindOf(err) != NotSynced {
		t.Fatal(err)
	}
	if _, err := svc.CreateRedirect(ctx, admin, RedirectInput{SourceDomain: "autodiscover.fresh.org", Target: "https://admin.test", Code: 302}); err != nil && KindOf(err) != NotSynced {
		t.Fatal(err)
	}
	if err := svc.sync(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := web.last()
	// bob's own site answers his name; the admin's redirect on bob's
	// other name does not take the setup away from bob.
	if route := firstRouteFor(t, cfg, "autoconfig.fresh.org"); isSetupRoute(route) {
		t.Fatalf("the owner's site does not answer: %s", route)
	}
	if route := firstRouteFor(t, cfg, "autodiscover.fresh.org"); !isSetupRoute(route) {
		t.Fatalf("another person's redirect answers the setup name: %s", route)
	}

	// A claim of somebody else that is already there (made before the
	// mail domain) does not answer the setup name either.
	if err := svc.DeleteSite(ctx, bob, "bobsetup"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.CreateRedirect(ctx, store.Redirect{SourceDomain: "autoconfig.fresh.org", Target: "https://attacker.test", Code: 302, OwnerID: carol.UserID}, 0); err != nil {
		t.Fatal(err)
	}
	if err := svc.sync(ctx); err != nil {
		t.Fatal(err)
	}
	if route := firstRouteFor(t, web.last(), "autoconfig.fresh.org"); !isSetupRoute(route) {
		t.Fatalf("an earlier claim of another person answers the setup name: %s", route)
	}
}

// A member cannot enable mail for a domain in which another user signs in
// with an address, even after its mail domain was deleted with the MX
// still pointing here.
func TestMailDomainWithOthersAccountAddress(t *testing.T) {
	w := newMailWorld(t)
	ctx := context.Background()
	svc := w.svc
	w.mx.point("company.example", testMailHost)
	if _, err := svc.UpdateUser(ctx, admin, admin.UserID, UserUpdate{Email: strp("alice@company.example")}); err != nil {
		t.Fatal(err)
	}
	if md, err := svc.EnableMailDomain(ctx, admin, "company.example"); err != nil || !md.Active {
		t.Fatalf("%+v %v", md, err)
	}
	if err := svc.DeleteMailDomain(ctx, admin, "company.example", "company.example"); err != nil && KindOf(err) != NotSynced {
		t.Fatal(err)
	}
	if _, err := svc.SaveSite(ctx, bob, "bobco", []string{"company.example"}); err != nil && KindOf(err) != NotSynced {
		t.Fatal(err)
	}
	_, err := svc.EnableMailDomain(ctx, bob, "company.example")
	wantKind(t, err, Invalid)
	// An address below the domain counts too.
	if _, err := svc.UpdateUser(ctx, admin, admin.UserID, UserUpdate{Email: strp("alice@eu.company.example")}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.EnableMailDomain(ctx, bob, "company.example")
	wantKind(t, err, Invalid)
	// Admins may; and a member's own address is no obstacle.
	if _, err := svc.UpdateUser(ctx, admin, admin.UserID, UserUpdate{Email: strp("")}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateUser(ctx, admin, bob.UserID, UserUpdate{Email: strp("bob@company.example")}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnableMailDomain(ctx, bob, "company.example"); err != nil {
		t.Fatal(err)
	}
}

// The reset link never goes into a mailbox here that a member other than
// the account holder controls.
func TestPasswordResetNotIntoOthersMailbox(t *testing.T) {
	w, alice := resetWorld(t)
	ctx := context.Background()
	svc := w.svc
	// alice's address is in bob's active mail domain (e.g. bob took the
	// domain over after alice set it).
	if _, err := svc.UpdateUser(ctx, admin, alice.ID, UserUpdate{Email: strp("alice@bobsite.example.com")}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RequestPasswordReset(ctx, "alice@bobsite.example.com", "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	if len(w.mail.sent) != 0 {
		t.Fatalf("reset link sent into another member's mailbox: %+v", w.mail.sent)
	}
	acts, _ := svc.Activities(ctx, admin, 5)
	if acts[0].Action != "account.password_reset_failed" {
		t.Fatalf("%+v", acts[0])
	}

	// The owner of the mail domain gets theirs; so does anybody whose
	// address is in an admin's mail domain.
	if _, err := svc.UpdateUser(ctx, admin, bob.UserID, UserUpdate{Email: strp("info@bobsite.example.com")}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RequestPasswordReset(ctx, "info@bobsite.example.com", "1.2.3.5"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateUser(ctx, admin, alice.ID, UserUpdate{Email: strp("alice@rootsite.example.com")}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RequestPasswordReset(ctx, "alice@rootsite.example.com", "1.2.3.6"); err != nil {
		t.Fatal(err)
	}
	var to []string
	for _, m := range w.mail.sent {
		to = append(to, m.To)
	}
	if !slices.Equal(to, []string{"info@bobsite.example.com", "alice@rootsite.example.com"}) {
		t.Fatalf("sent to %v", to)
	}
}

// Files of a site whose removal could not finish are removed by the next
// sync, and a new site of the same name never starts with them.
func TestDeletedSiteFilesNotInherited(t *testing.T) {
	ctx := context.Background()
	for _, syncFirst := range []bool{false, true} {
		svc, web, sitesDir := newService(t)
		bobU := addUser(t, svc, "bob", store.RoleMember)
		bobA := Actor{UserID: bobU.ID, Name: "bob", Via: ViaSession}
		if _, err := svc.SaveSite(ctx, admin, "private", []string{"private.example.org"}); err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{"v1", "v2"} {
			if _, err := svc.Publish(ctx, admin, "private", tarGz(t, map[string]string{"index.html": v})); err != nil {
				t.Fatal(err)
			}
		}
		web.setFail(errors.New("down"))
		wantKind(t, svc.DeleteSite(ctx, admin, "private"), NotSynced)
		web.setFail(nil)
		dir := filepath.Join(sitesDir, "private")
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("files removed while still served: %v", err)
		}
		if syncFirst {
			if err := svc.Sync(ctx, System); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("files of the deleted site left after a sync: %v", err)
			}
		}
		site, err := svc.SaveSite(ctx, bobA, "private", []string{"bob.example.org"})
		if err != nil {
			t.Fatal(err)
		}
		if site.Current != "" || len(site.Releases) != 0 {
			t.Fatalf("new site inherited releases: %+v", site)
		}
		if strings.Contains(web.last(), filepath.Join(dir, "current")) && !strings.Contains(web.last(), `"This site has no content yet.\n"`) {
			t.Fatalf("old files served: %s", web.last())
		}
	}
}
