package store

import (
	"errors"
	"testing"
)

func TestRedirectsMigrationAndCRUD(t *testing.T) {
	s, _ := open(t)
	alice, err := s.CreateUser(ctx, "alice", "pw-hash", RoleAdmin, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "carol", "pw-hash", RoleAdmin, false); err != nil {
		t.Fatal(err)
	}
	bob, err := s.CreateUser(ctx, "bob", "pw-hash", RoleMember, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSite(ctx, "blog", []string{"blog.example.com", "www.blog.example.com"}, bob.ID); err != nil {
		t.Fatal(err)
	}

	// Domain redirect, owned by its creator.
	d, err := s.CreateRedirect(ctx, Redirect{OwnerID: alice.ID, SiteName: "ignored", SourceDomain: "old.example.com",
		Target: "https://example.com", Code: 301, KeepPath: true, KeepQuery: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID == 0 || d.OwnerID != alice.ID || d.Owner != "alice" || d.SiteName != "" || d.SourcePath != "" ||
		d.Code != 301 || !d.KeepPath || !d.KeepQuery || d.CreatedAt.IsZero() {
		t.Fatalf("domain redirect %+v", d)
	}
	// Path redirect, owned by the site's owner whatever is passed.
	p, err := s.CreateRedirect(ctx, Redirect{OwnerID: alice.ID, SiteName: "blog", SourceDomain: "blog.example.com",
		SourcePath: "/old", Target: "/new", Code: 308}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.OwnerID != bob.ID || p.Owner != "bob" || p.SiteName != "blog" {
		t.Fatalf("path redirect %+v", p)
	}

	// Conflicts.
	if _, err := s.CreateRedirect(ctx, Redirect{OwnerID: bob.ID, SourceDomain: "old.example.com", Target: "https://x.example", Code: 302}, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("second domain redirect: %v", err)
	}
	if _, err := s.CreateRedirect(ctx, Redirect{SiteName: "blog", SourceDomain: "blog.example.com", SourcePath: "/old", Target: "/x", Code: 302}, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("same path twice: %v", err)
	}
	var taken *DomainTakenError
	if _, err := s.CreateRedirect(ctx, Redirect{OwnerID: bob.ID, SourceDomain: "blog.example.com", Target: "https://x.example", Code: 302}, 0); !errors.As(err, &taken) || taken.Site != "blog" {
		t.Fatalf("domain redirect on a site domain: %v", err)
	}
	if _, err := s.CreateRedirect(ctx, Redirect{SiteName: "blog", SourceDomain: "other.example.com", SourcePath: "/a", Target: "/b", Code: 302}, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("path redirect on a foreign domain: %v", err)
	}
	// A site cannot take a domain that redirects.
	if _, err := s.SetDomains(ctx, "blog", []string{"blog.example.com", "old.example.com"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("site domain that redirects: %v", err)
	}

	// Limit: bob owns one (the path redirect).
	if _, err := s.CreateRedirect(ctx, Redirect{OwnerID: bob.ID, SourceDomain: "b.example.com", Target: "https://x.example", Code: 302}, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("limit: %v", err)
	}
	if _, err := s.CreateRedirect(ctx, Redirect{OwnerID: bob.ID, SourceDomain: "b.example.com", Target: "https://x.example", Code: 302}, 2); err != nil {
		t.Fatalf("under the limit: %v", err)
	}

	// Update.
	p.SourcePath, p.Target, p.Code, p.KeepPath = "/docs/*", "https://docs.example.com", 307, true
	u, err := s.UpdateRedirect(ctx, p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if u.SourcePath != "/docs/*" || u.Target != "https://docs.example.com" || u.Code != 307 || !u.KeepPath || u.CreatedAt != p.CreatedAt {
		t.Fatalf("updated %+v", u)
	}
	if _, err := s.UpdateRedirect(ctx, Redirect{ID: 999, SourceDomain: "z.example.com", Target: "https://x", Code: 301}, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	all, err := s.Redirects(ctx)
	if err != nil || len(all) != 3 || all[0].SourceDomain != "b.example.com" || all[1].SourceDomain != "blog.example.com" {
		t.Fatalf("list %+v %v", all, err)
	}

	// Users with domain redirects cannot be deleted.
	if err := s.DeleteUser(ctx, alice.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete alice: %v", err)
	}

	// Dropping the domain from the site drops its path redirects; so does
	// deleting the site.
	if _, err := s.CreateRedirect(ctx, Redirect{SiteName: "blog", SourceDomain: "www.blog.example.com", SourcePath: "/", Target: "https://blog.example.com/", Code: 301}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDomains(ctx, "blog", []string{"blog.example.com"}); err != nil {
		t.Fatal(err)
	}
	all, _ = s.Redirects(ctx)
	if len(all) != 3 {
		t.Fatalf("after dropping a domain: %+v", all)
	}
	if err := s.DeleteSite(ctx, "blog"); err != nil {
		t.Fatal(err)
	}
	all, _ = s.Redirects(ctx)
	if len(all) != 2 || all[0].SiteName != "" || all[1].SiteName != "" {
		t.Fatalf("after deleting the site: %+v", all)
	}

	if err := s.DeleteRedirect(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRedirect(ctx, d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if _, err := s.Redirect(ctx, d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
	if err := s.DeleteUser(ctx, alice.ID); err != nil {
		t.Fatalf("delete alice without redirects: %v", err)
	}
}

func TestRedirectOfOwnerlessSiteIsAdopted(t *testing.T) {
	s, _ := open(t)
	if _, err := s.CreateSite(ctx, "old", []string{"old.example.com"}, 0); err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateRedirect(ctx, Redirect{SiteName: "old", SourceDomain: "old.example.com", SourcePath: "/a", Target: "/b", Code: 301}, 500)
	if err != nil || r.OwnerID != 0 || r.Owner != "" {
		t.Fatalf("%+v %v", r, err)
	}
	admin, err := s.CreateUser(ctx, "root", "pw-hash", RoleAdmin, true)
	if err != nil {
		t.Fatal(err)
	}
	r, _ = s.Redirect(ctx, r.ID)
	if r.OwnerID != admin.ID || r.Owner != "root" {
		t.Fatalf("after the first admin: %+v", r)
	}
}
