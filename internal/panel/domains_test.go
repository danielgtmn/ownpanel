package panel

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestDomainsPermissionMatrix(t *testing.T) {
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
	other := func(a Actor) int64 {
		if a.UserID == 1 {
			return 2
		}
		return 1
	}
	ops := []struct {
		name string
		run  func(w world, a Actor) error
		want []Kind
	}{
		{"list", func(w world, a Actor) error { _, err := w.svc.Domains(ctx, a); return err },
			[]Kind{ok, ok, ok, ok, F, ok, F, U}},
		{"add", func(w world, a Actor) error { _, err := w.svc.AddDomain(ctx, a, "elsewhere.org"); return err },
			[]Kind{ok, ok, ok, F, F, F, F, U}},
		{"remove own", func(w world, a Actor) error {
			if a.UserID != 0 {
				if _, err := w.svc.store.AddDomain(ctx, a.UserID, "elsewhere.org"); err != nil {
					t.Fatal(err)
				}
			}
			_, err := w.svc.RemoveDomain(ctx, a, "elsewhere.org")
			return err
		}, []Kind{ok, ok, ok, F, F, F, F, U}},
		{"remove other's", func(w world, a Actor) error {
			if _, err := w.svc.store.AddDomain(ctx, other(a), "elsewhere.org"); err != nil {
				t.Fatal(err)
			}
			_, err := w.svc.RemoveDomain(ctx, a, "elsewhere.org")
			return err
		}, []Kind{N, N, N, F, F, F, F, U}},
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

func TestDomainsWithDNSElsewhere(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	names := func(a Actor) []string {
		t.Helper()
		list, err := w.svc.Domains(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, d := range list {
			out = append(out, d.Name)
		}
		return out
	}

	for _, bad := range []string{"", "203.0.113.5", "www.example.com", "bücher.de", "not a domain", "com"} {
		if _, err := w.svc.AddDomain(ctx, bob, bad); KindOf(err) != Invalid {
			t.Fatalf("add %q: %v", bad, err)
		}
	}

	// Added once, normalized; adding again is fine.
	for range 2 {
		got, err := w.svc.AddDomain(ctx, bob, " Elsewhere.ORG. ")
		if err != nil || got.Name != "elsewhere.org" || got.CreatedAt.IsZero() || len(got.UsedBy) != 0 {
			t.Fatalf("add: %+v %v", got, err)
		}
	}
	if _, err := w.svc.AddDomain(ctx, bob, "example.com"); err != nil {
		t.Fatal(err)
	}
	if got := names(bob); !reflect.DeepEqual(got, []string{"elsewhere.org", "example.com"}) {
		t.Fatalf("bob's domains: %v", got)
	}
	// Only bob's own sites count as use; root's site in example.com does not.
	list, _ := w.svc.Domains(ctx, bob)
	if used := list[1].UsedBy; !reflect.DeepEqual(used,
		[]string{"the site bobother (bobother.example.com)", "the site bobsite (bobsite.example.com)"}) {
		t.Fatalf("example.com used by: %v", used)
	}
	// Per person: root sees none of bob's entries.
	if got := names(admin); len(got) != 0 {
		t.Fatalf("root sees: %v", got)
	}

	// Removing an entry that is still used succeeds and says what keeps it.
	removed, err := w.svc.RemoveDomain(ctx, bob, "EXAMPLE.com")
	if err != nil || removed.Name != "example.com" || len(removed.UsedBy) != 2 ||
		!strings.Contains(removed.UsedBy[0], "the site bobother") {
		t.Fatalf("remove used: %+v %v", removed, err)
	}
	removed, err = w.svc.RemoveDomain(ctx, bob, "elsewhere.org")
	if err != nil || len(removed.UsedBy) != 0 {
		t.Fatalf("remove: %+v %v", removed, err)
	}
	if _, err := w.svc.RemoveDomain(ctx, bob, "elsewhere.org"); KindOf(err) != NotFound {
		t.Fatalf("remove twice: %v", err)
	}
	if got := names(bob); len(got) != 0 {
		t.Fatalf("after removing: %v", got)
	}

	acts, err := w.svc.store.Activities(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, a := range acts {
		if strings.HasPrefix(a.Action, "domain.") {
			seen = append(seen, a.Action+" "+a.Target)
		}
	}
	want := []string{"domain.remove elsewhere.org", "domain.remove example.com", "domain.add example.com", "domain.add elsewhere.org"}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("activity: %v, want %v", seen, want)
	}
}

// Managing the DNS of a domain entered by hand, by choosing its zone or by
// creating it, forgets the hand-made entry; someone else's entry stays.
func TestManagingDNSDropsTheHandEntry(t *testing.T) {
	d := newDNSWorld(t)
	ctx := context.Background()
	names := func(a Actor) []string {
		t.Helper()
		list, err := d.svc.Domains(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, x := range list {
			out = append(out, x.Name)
		}
		return out
	}
	for _, name := range []string{"other.org", "fresh.org", "kept.org"} {
		if _, err := d.svc.AddDomain(ctx, bob, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.svc.AddDomain(ctx, admin, "other.org"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.svc.AdoptDNSZone(ctx, bob, d.bobConn, "other.org"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.svc.CreateDNSZone(ctx, bob, d.bobConn, "fresh.org"); err != nil {
		t.Fatal(err)
	}
	if got := names(bob); !reflect.DeepEqual(got, []string{"kept.org"}) {
		t.Fatalf("bob's entries: %v", got)
	}
	if got := names(admin); !reflect.DeepEqual(got, []string{"other.org"}) {
		t.Fatalf("someone else's entry: %v", got)
	}
	// Choosing a zone without an entry is fine.
	if _, err := d.svc.AdoptDNSZone(ctx, bob, d.bobConn, "other.org"); err != nil {
		t.Fatal(err)
	}
}
