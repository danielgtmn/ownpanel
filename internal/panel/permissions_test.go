package panel

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/store"
)

// Users of the permission tests: root (1, admin, from newService),
// bob (2, member). Sites: rootsite (root), bobsite and bobother (bob).
var (
	bob          = Actor{UserID: "2", Name: "bob", Via: ViaSession}
	bobRead      = Actor{UserID: "2", Name: "bob", Via: ViaToken, ReadOnly: true, tokenName: "r"}
	bobLimited   = Actor{UserID: "2", Name: "bob", Via: ViaToken, Sites: []string{"bobsite"}, tokenName: "l"}
	rootRead     = Actor{UserID: "1", Name: "root", Admin: true, Via: ViaToken, ReadOnly: true, tokenName: "r"}
	rootLimited  = Actor{UserID: "1", Name: "root", Admin: true, Via: ViaToken, Sites: []string{"rootsite"}, tokenName: "l"}
	rootWriteTok = Actor{UserID: "1", Name: "root", Admin: true, Via: ViaToken, tokenName: "w"}
	nobody       = Actor{}
)

type world struct {
	svc     *Service
	release string // the live release of bobsite
}

func newWorld(t *testing.T) world {
	t.Helper()
	svc, _, _ := newService(t)
	ctx := context.Background()
	addUser(t, svc, "bob", store.RoleMember)
	for _, c := range []struct {
		a    Actor
		name string
	}{{admin, "rootsite"}, {bob, "bobsite"}, {bob, "bobother"}} {
		if _, err := svc.SaveSite(ctx, c.a, c.name, []string{c.name + ".example.com"}); err != nil {
			t.Fatal(err)
		}
	}
	site, err := svc.Publish(ctx, bob, "bobsite", tarGz(t, map[string]string{"index.html": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	return world{svc: svc, release: site.Current}
}

const ok Kind = -1

func TestPermissionMatrix(t *testing.T) {
	ctx := context.Background()
	actors := []struct {
		name string
		a    Actor
	}{
		{"admin", admin}, {"admin write token", rootWriteTok}, {"member", bob}, {"member read token", bobRead},
		{"member site token", bobLimited}, {"admin read token", rootRead}, {"admin site token", rootLimited},
		{"nobody", nobody},
	}
	ops := []struct {
		name string
		run  func(w world, a Actor) error
		want []Kind // per actor, in the order above
	}{
		{"get own site of member", func(w world, a Actor) error { _, err := w.svc.Site(ctx, a, "bobsite"); return err },
			[]Kind{ok, ok, ok, ok, ok, ok, NotFound, Unauthenticated}},
		{"get admin's site", func(w world, a Actor) error { _, err := w.svc.Site(ctx, a, "rootsite"); return err },
			[]Kind{ok, ok, NotFound, NotFound, NotFound, ok, ok, Unauthenticated}},
		{"get member's other site", func(w world, a Actor) error { _, err := w.svc.Site(ctx, a, "bobother"); return err },
			[]Kind{ok, ok, ok, ok, NotFound, ok, NotFound, Unauthenticated}},
		{"change domains", func(w world, a Actor) error {
			_, err := w.svc.SaveSite(ctx, a, "bobsite", []string{"new.example.com"})
			return err
		}, []Kind{ok, ok, ok, Forbidden, ok, Forbidden, Conflict, Unauthenticated}},
		{"save over an invisible site", func(w world, a Actor) error {
			_, err := w.svc.SaveSite(ctx, a, "rootsite", nil)
			return err
		}, []Kind{ok, ok, Conflict, Conflict, Conflict, Forbidden, ok, Unauthenticated}},
		{"create site", func(w world, a Actor) error { _, err := w.svc.SaveSite(ctx, a, "fresh", nil); return err },
			[]Kind{ok, ok, ok, Forbidden, Forbidden, Forbidden, Forbidden, Unauthenticated}},
		{"publish", func(w world, a Actor) error {
			_, err := w.svc.Publish(ctx, a, "bobsite", strings.NewReader("not read when refused"))
			if KindOf(err) == Invalid { // permitted; the archive itself is bad
				return nil
			}
			return err
		}, []Kind{ok, ok, ok, Forbidden, ok, Forbidden, NotFound, Unauthenticated}},
		{"make live", func(w world, a Actor) error { _, err := w.svc.MakeLive(ctx, a, "bobsite", w.release); return err },
			[]Kind{ok, ok, ok, Forbidden, ok, Forbidden, NotFound, Unauthenticated}},
		{"delete site", func(w world, a Actor) error { return w.svc.DeleteSite(ctx, a, "bobsite") },
			[]Kind{ok, ok, ok, Forbidden, Forbidden, Forbidden, NotFound, Unauthenticated}},
		{"delete invisible site", func(w world, a Actor) error { return w.svc.DeleteSite(ctx, a, "rootsite") },
			[]Kind{ok, ok, NotFound, NotFound, NotFound, Forbidden, Forbidden, Unauthenticated}},
		{"list users", func(w world, a Actor) error { _, err := w.svc.Users(ctx, a); return err },
			[]Kind{ok, ok, Forbidden, Forbidden, Forbidden, Forbidden, Forbidden, Unauthenticated}},
		{"create user", func(w world, a Actor) error {
			_, err := w.svc.CreateUser(ctx, a, NewUser{Name: "newbie", Role: "member"})
			if KindOf(err) == Invalid { // permitted; empty password refused
				return nil
			}
			return err
		}, []Kind{ok, ok, Forbidden, Forbidden, Forbidden, Forbidden, Forbidden, Unauthenticated}},
		{"delete user", func(w world, a Actor) error { return w.svc.DeleteUser(ctx, a, "2") },
			[]Kind{Conflict, Conflict, Forbidden, Forbidden, Forbidden, Forbidden, Forbidden, Unauthenticated}},
		{"activity", func(w world, a Actor) error { _, err := w.svc.Activities(ctx, a, 10); return err },
			[]Kind{ok, ok, Forbidden, Forbidden, Forbidden, Forbidden, Forbidden, Unauthenticated}},
		{"list own tokens", func(w world, a Actor) error { _, err := w.svc.Tokens(ctx, a); return err },
			[]Kind{ok, Forbidden, ok, Forbidden, Forbidden, Forbidden, Forbidden, Unauthenticated}},
		{"create token", func(w world, a Actor) error { _, err := w.svc.CreateToken(ctx, a, "t", "read", nil, 0); return err },
			[]Kind{ok, Forbidden, ok, Forbidden, Forbidden, Forbidden, Forbidden, Unauthenticated}},
		{"change password", func(w world, a Actor) error {
			err := w.svc.ChangePassword(ctx, a, "wrong", "irrelevant-password")
			if KindOf(err) == Invalid { // permitted; the current password is wrong
				return nil
			}
			return err
		}, []Kind{ok, Forbidden, ok, Forbidden, Forbidden, Forbidden, Forbidden, Unauthenticated}},
		{"set own e-mail address", func(w world, a Actor) error {
			_, err := w.svc.SetOwnEmail(ctx, a, "wrong", "me@example.org")
			if KindOf(err) == Invalid { // permitted; the current password is wrong
				return nil
			}
			return err
		},
			[]Kind{ok, Forbidden, ok, Forbidden, Forbidden, Forbidden, Forbidden, Unauthenticated}},
		{"set another person's e-mail address", func(w world, a Actor) error {
			e := "bob@example.org"
			_, err := w.svc.UpdateUser(ctx, a, "2", UserUpdate{Email: &e})
			return err
		}, []Kind{ok, ok, Forbidden, Forbidden, Forbidden, Forbidden, Forbidden, Unauthenticated}},
		{"sync", func(w world, a Actor) error { return w.svc.Sync(ctx, a) },
			[]Kind{ok, ok, Forbidden, Forbidden, Forbidden, Forbidden, Forbidden, Unauthenticated}},
	}
	for _, op := range ops {
		for i, ac := range actors {
			t.Run(op.name+"/"+ac.name, func(t *testing.T) {
				w := newWorld(t)
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

func TestSitesListPerActor(t *testing.T) {
	w := newWorld(t)
	for _, tc := range []struct {
		name string
		a    Actor
		want []string
	}{
		{"admin", admin, []string{"bobother", "bobsite", "rootsite"}},
		{"member", bob, []string{"bobother", "bobsite"}},
		{"member read token", bobRead, []string{"bobother", "bobsite"}},
		{"member site token", bobLimited, []string{"bobsite"}},
		{"admin site token", rootLimited, []string{"rootsite"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list, err := w.svc.Sites(context.Background(), tc.a)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, s := range list {
				names = append(names, s.Name)
			}
			if !reflect.DeepEqual(names, tc.want) {
				t.Fatalf("got %v, want %v", names, tc.want)
			}
		})
	}
	_, err := w.svc.Sites(context.Background(), nobody)
	wantKind(t, err, Unauthenticated)
}

func TestNewSiteBelongsToCreator(t *testing.T) {
	w := newWorld(t)
	site, err := w.svc.SaveSite(context.Background(), bob, "mine", nil)
	if err != nil || site.Owner != "bob" {
		t.Fatalf("%+v %v", site, err)
	}
	_, err = w.svc.SaveSite(context.Background(), System, "system-made", nil)
	wantKind(t, err, Forbidden)
}

func TestDomainOfInvisibleSiteDoesNotLeakItsName(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	_, err := w.svc.SaveSite(ctx, bob, "bobsite", []string{"rootsite.example.com"})
	wantKind(t, err, Invalid)
	if strings.Contains(err.Error(), "rootsite\"") || !strings.Contains(err.Error(), "another site") {
		t.Fatalf("message %q", err)
	}
	_, err = w.svc.SaveSite(ctx, admin, "rootsite", []string{"bobsite.example.com"})
	if !strings.Contains(err.Error(), `site "bobsite"`) {
		t.Fatalf("admins may see which site: %q", err)
	}
}

func TestPanelDomainIsReserved(t *testing.T) {
	svc, _, _ := newService(t)
	svc.opts.PanelDomain, svc.opts.PanelUpstream = "panel.example.com", "127.0.0.1:8600"
	_, err := svc.SaveSite(context.Background(), admin, "x", []string{"Panel.Example.com"})
	wantKind(t, err, Invalid)

	// A site that already holds the domain stops the daemon from starting.
	if _, err := svc.SaveSite(context.Background(), admin, "y", []string{"taken.example.com"}); err != nil {
		t.Fatal(err)
	}
	opts := svc.opts
	opts.PanelDomain, opts.PanelUpstream = "taken.example.com", "127.0.0.1:8600"
	if _, err := New(Config{Store: svc.store, Deploys: svc.deploys, Web: &fakeWeb{}, Caddy: opts}); err == nil {
		t.Fatal("panel domain used by a site must be refused")
	}
}

func TestTokenSitesMustBeVisible(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	_, err := w.svc.CreateToken(ctx, bob, "t", "write", []string{"bobsite", "rootsite"}, 0)
	wantKind(t, err, Invalid)
	_, err = w.svc.CreateToken(ctx, bob, "t", "write", []string{}, 0)
	wantKind(t, err, Invalid)
	_, err = w.svc.CreateToken(ctx, bob, "t", "admin", nil, 0)
	wantKind(t, err, Invalid)
	nt, err := w.svc.CreateToken(ctx, bob, " deploy ", "write", []string{"bobsite", "bobsite"}, 0)
	if err != nil || nt.Name != "deploy" || !reflect.DeepEqual(nt.Sites, []string{"bobsite"}) {
		t.Fatalf("%+v %v", nt, err)
	}
}
