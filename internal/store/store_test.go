package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"
	"time"

	"github.com/danielgtmn/ablage"
	"github.com/danielgtmn/ablage/memstore"
	"github.com/danielgtmn/sesam/v2"
)

var ctx = context.Background()

// eachStore runs fn on a store in memory and on SQLite.
func eachStore(t *testing.T, fn func(t *testing.T, s *Store)) {
	t.Run("memory", func(t *testing.T) {
		s, err := New(memstore.New())
		if err != nil {
			t.Fatal(err)
		}
		fn(t, s)
	})
	t.Run("sqlite", func(t *testing.T) {
		s, _ := open(t)
		fn(t, s)
	})
}

func open(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "ownpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func TestOpenCreatesPrivateFile(t *testing.T) {
	_, dir := open(t)
	fi, err := os.Stat(filepath.Join(dir, "ownpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

func TestMigrateAppliesInOrderAndOnce(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// 0002 depends on 0001, so applying them out of order would fail.
	fsys := fstest.MapFS{
		"0002_more.sql": {Data: []byte(`INSERT INTO t (v) VALUES ('second');`)},
		"0001_init.sql": {Data: []byte(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT); INSERT INTO t (v) VALUES ('first');`)},
		"README.md":     {Data: []byte(`not a migration`)},
	}
	for i := 0; i < 2; i++ {
		if err := migrate(db, fsys); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	var got []string
	rows, err := db.Query(`SELECT v FROM t ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v string
		rows.Scan(&v)
		got = append(got, v)
	}
	rows.Close()
	if !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("rows %v", got)
	}

	// A new file is picked up later; a broken one rolls back completely.
	fsys["0003_broken.sql"] = &fstest.MapFile{Data: []byte(`INSERT INTO t (v) VALUES ('third'); SELECT * FROM nope;`)}
	if err := migrate(db, fsys); err == nil {
		t.Fatal("broken migration must fail")
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM t`).Scan(&n)
	if n != 2 {
		t.Fatalf("broken migration left %d rows", n)
	}
	db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n)
	if n != 2 {
		t.Fatalf("schema_migrations has %d rows", n)
	}
}

func TestReopenKeepsData(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "ownpanel.db")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSite(ctx, "blog", []string{"blog.example.com"}, ""); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if site, err := s.Site(ctx, "blog"); err != nil || site.Domains[0] != "blog.example.com" {
		t.Fatalf("%+v %v", site, err)
	}
}

func TestSites(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		admin, err := s.CreateUser(ctx, "admin", "", RoleAdmin, true)
		if err != nil {
			t.Fatal(err)
		}
		site, err := s.CreateSite(ctx, "blog", []string{"WWW.Example.com.", "example.com", "example.com"}, admin.ID)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"example.com", "www.example.com"}; !reflect.DeepEqual(site.Domains, want) || site.Owner != "admin" {
			t.Fatalf("site %+v", site)
		}
		_, err = s.CreateSite(ctx, "blog", nil, admin.ID)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate name: %v", err)
		}
		_, err = s.CreateSite(ctx, "shop", []string{"example.com"}, admin.ID)
		var taken *DomainTakenError
		if !errors.As(err, &taken) || taken.Site != "blog" || !errors.Is(err, ErrInvalid) {
			t.Fatalf("domain of another site: %v", err)
		}
		if _, err := s.Site(ctx, "shop"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("failed create must leave nothing behind: %v", err)
		}
		if site, err = s.SetDomains(ctx, "blog", []string{"blog.example.com"}); err != nil || len(site.Domains) != 1 {
			t.Fatalf("%+v %v", site, err)
		}
		if _, err := s.SetDomains(ctx, "ghost", nil); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ghost: %v", err)
		}
		if _, err := s.CreateSite(ctx, "shop", []string{"example.com"}, admin.ID); err != nil {
			t.Fatalf("freed domain: %v", err)
		}
		list, _ := s.Sites(ctx)
		if len(list) != 2 || list[0].Name != "blog" || list[1].Name != "shop" {
			t.Fatalf("list %+v", list)
		}

		tok, err := s.CreateToken(ctx, Token{UserID: admin.ID, Name: "t", Hash: "x", Access: "write", Sites: []string{"blog", "shop"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteSite(ctx, "blog"); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteSite(ctx, "blog"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second delete: %v", err)
		}
		got, _, err := s.TokenByHash(ctx, "x")
		if err != nil || !reflect.DeepEqual(got.Sites, []string{"shop"}) || got.ID != tok.ID {
			t.Fatalf("token sites after delete: %+v %v", got, err)
		}
	})
}

func TestUsers(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		if _, err := s.CreateSite(ctx, "old", nil, ""); err != nil {
			t.Fatal(err)
		}
		admin, err := s.CreateUser(ctx, "admin", "", RoleAdmin, true)
		if err != nil {
			t.Fatal(err)
		}
		if site, _ := s.Site(ctx, "old"); site.OwnerID != admin.ID {
			t.Fatalf("first admin must own ownerless sites: %+v", site)
		}
		if _, err := s.CreateUser(ctx, "second", "", RoleAdmin, true); !errors.Is(err, ErrConflict) {
			t.Fatalf("firstOnly with users present: %v", err)
		}
		if _, err := s.CreateUser(ctx, "admin", "", RoleMember, false); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate name: %v", err)
		}
		member, err := s.CreateUser(ctx, "bob", "", RoleMember, false)
		if err != nil {
			t.Fatal(err)
		}

		if err := s.SetRole(ctx, admin.ID, RoleMember); !errors.Is(err, ErrConflict) {
			t.Fatalf("demote last admin: %v", err)
		}
		if err := s.DeleteUser(ctx, admin.ID); !errors.Is(err, ErrConflict) {
			t.Fatalf("delete last admin: %v", err)
		}
		if _, err := s.CreateSite(ctx, "bobs", nil, member.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteUser(ctx, member.ID); !errors.Is(err, ErrConflict) {
			t.Fatalf("delete owner of sites: %v", err)
		}
		s.DeleteSite(ctx, "bobs")
		if _, err := s.CreateToken(ctx, Token{UserID: member.ID, Name: "t", Hash: "bobhash", Access: "read"}); err != nil {
			t.Fatal(err)
		}
		if err := s.AddOldSession(ctx, member.ID, OldSession{ID: "sess", CreatedAt: time.Unix(0, 0), ExpiresAt: time.Unix(4000000000, 0)}); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteUser(ctx, member.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.TokenByHash(ctx, "bobhash"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("token of deleted user: %v", err)
		}
		if old, err := s.OldSessions(ctx, member.ID, time.Now()); err != nil || len(old) != 0 {
			t.Fatalf("session of deleted user: %v %v", old, err)
		}

		// With a second admin the first can step down.
		other, _ := s.CreateUser(ctx, "carol", "", RoleAdmin, false)
		if err := s.SetRole(ctx, admin.ID, RoleMember); err != nil {
			t.Fatal(err)
		}
		if err := s.SetRole(ctx, other.ID, RoleMember); !errors.Is(err, ErrConflict) {
			t.Fatalf("now carol is the last admin: %v", err)
		}
	})
}

func TestTokens(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		u, _ := s.CreateUser(ctx, "admin", "", RoleAdmin, true)
		now := time.Unix(1_800_000_000, 0)
		tok, _ := s.CreateToken(ctx, Token{UserID: u.ID, Name: "ci", Hash: "th", Access: "write"})
		if tok.Sites != nil {
			t.Fatal("nil sites must stay nil")
		}
		touch := func(at time.Time) *time.Time {
			t.Helper()
			if err := s.TouchToken(ctx, tok.ID, at); err != nil {
				t.Fatal(err)
			}
			got, _, _ := s.TokenByHash(ctx, "th")
			return got.LastUsedAt
		}
		if got := touch(now); got == nil || !got.Equal(now) {
			t.Fatalf("first use: %v", got)
		}
		if got := touch(now.Add(30 * time.Second)); !got.Equal(now) {
			t.Fatalf("second use within a minute must not write: %v", got)
		}
		if got := touch(now.Add(61 * time.Second)); !got.Equal(now.Add(61 * time.Second)) {
			t.Fatalf("use after a minute: %v", got)
		}
		if err := s.DeleteToken(ctx, u.ID+"x", tok.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("other user's token: %v", err)
		}
		if err := s.DeleteToken(ctx, u.ID, tok.ID); err != nil {
			t.Fatal(err)
		}
	})
}

func TestImportStateFile(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		dir := t.TempDir()
		file := filepath.Join(dir, "state.json")
		if n, err := s.ImportStateFile(ctx, file); err != nil || n != 0 {
			t.Fatalf("missing file: %d %v", n, err)
		}
		os.WriteFile(file, []byte(`[{"name":"blog","domains":["blog.example.com"]},{"name":"shop","domains":[]}]`), 0o600)
		n, err := s.ImportStateFile(ctx, file)
		if err != nil || n != 2 {
			t.Fatalf("import: %d %v", n, err)
		}
		if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("state.json must be renamed")
		}
		if _, err := os.Stat(file + ".imported"); err != nil {
			t.Fatal(err)
		}
		site, err := s.Site(ctx, "blog")
		if err != nil || site.OwnerID != "" || site.Domains[0] != "blog.example.com" {
			t.Fatalf("%+v %v", site, err)
		}
		admin, _ := s.CreateUser(ctx, "admin", "", RoleAdmin, true)
		if site, _ := s.Site(ctx, "shop"); site.OwnerID != admin.ID {
			t.Fatalf("first admin takes over imported sites: %+v", site)
		}

		// With sites in the database a state file is left alone.
		os.WriteFile(file, []byte(`[{"name":"other","domains":[]}]`), 0o600)
		if n, err := s.ImportStateFile(ctx, file); err != nil || n != 0 {
			t.Fatalf("second import: %d %v", n, err)
		}
		if _, err := os.Stat(file); err != nil {
			t.Fatal("file must stay when nothing was imported")
		}
	})
}

func TestImportOwnedByExistingAdmin(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		dir := t.TempDir()
		admin, _ := s.CreateUser(ctx, "admin", "", RoleAdmin, true)
		file := filepath.Join(dir, "state.json")
		os.WriteFile(file, []byte(`[{"name":"blog","domains":["blog.example.com"]}]`), 0o600)
		if _, err := s.ImportStateFile(ctx, file); err != nil {
			t.Fatal(err)
		}
		if site, _ := s.Site(ctx, "blog"); site.OwnerID != admin.ID {
			t.Fatalf("%+v", site)
		}
	})
}

func TestVacuumInto(t *testing.T) {
	s, dir := open(t)
	s.CreateSite(ctx, "blog", []string{"blog.example.com"}, "")
	dest := filepath.Join(dir, "copy.db")
	if err := VacuumInto(filepath.Join(dir, "ownpanel.db"), dest); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Site(ctx, "blog"); err != nil {
		t.Fatal(err)
	}
	if err := VacuumInto(filepath.Join(dir, "missing.db"), filepath.Join(dir, "x.db")); err == nil {
		t.Fatal("missing database must fail, not be created")
	}
}

func TestActivities(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		for _, a := range []string{"one", "two", "three"} {
			if err := s.AddActivity(ctx, Activity{Actor: "admin", Action: a}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.Activities(ctx, 2)
		if err != nil || len(got) != 2 || got[0].Action != "three" || got[1].Action != "two" {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

// signInUser adds a sign-in user (sesam's auth_users) and returns its id.
func signInUser(t *testing.T, s *Store, name string) string {
	t.Helper()
	auth, err := sesam.New(sesam.Config{Store: s.Ablage(), BaseURL: "http://localhost"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := auth.CreateUser(ctx, sesam.NewUser{Username: name})
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// TestAuthUserReference: a panel user can only point at a sign-in user that
// exists, and the sign-in user cannot be deleted while it is linked.
func TestAuthUserReference(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		if _, err := s.CreateUser(ctx, "ghost", "no-such-sign-in", RoleAdmin, false); !errors.Is(err, ablage.ErrReference) {
			t.Fatalf("dangling link: %v", err)
		}
		auth, err := sesam.New(sesam.Config{Store: s.Ablage(), BaseURL: "http://localhost"})
		if err != nil {
			t.Fatal(err)
		}
		au := signInUser(t, s, "anna")
		u, err := s.CreateUser(ctx, "anna", au, RoleAdmin, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := auth.DeleteUser(ctx, au); !errors.Is(err, ablage.ErrReference) {
			t.Fatalf("deleting a linked sign-in user: %v", err)
		}
		// Panel user first, then the sign-in user (as the panel does). The
		// last admin cannot be deleted, so there is another one.
		if _, err := s.CreateUser(ctx, "admin2", "", RoleAdmin, false); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteUser(ctx, u.ID); err != nil {
			t.Fatal(err)
		}
		if err := auth.DeleteUser(ctx, au); err != nil {
			t.Fatal(err)
		}
	})
}

// TestMoveToAuth: users from before the auth tables are listed with their
// hash and live sessions until they are linked; linking forgets both.
func TestMoveToAuth(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		now := time.Unix(1_800_000_000, 0)
		ou, err := s.AddUnmovedUser(ctx, "old", "$argon2id$x", RoleAdmin)
		if err != nil {
			t.Fatal(err)
		}
		id := ou.ID
		for _, row := range []struct {
			id      string
			expires int64
		}{{"live", now.Unix() + 60}, {"gone", now.Unix() - 1}} {
			if err := s.AddOldSession(ctx, id, OldSession{ID: row.id, CreatedAt: now.Add(-100 * time.Second), ExpiresAt: time.Unix(row.expires, 0)}); err != nil {
				t.Fatal(err)
			}
		}
		authNew, authOld, authOther := signInUser(t, s, "new"), signInUser(t, s, "old"), signInUser(t, s, "other")
		if _, err := s.CreateUser(ctx, "new", authNew, RoleMember, false); err != nil {
			t.Fatal(err)
		}
		un, err := s.UnmovedUsers(ctx)
		if err != nil || len(un) != 1 || un[0].Name != "old" || un[0].PasswordHash != "$argon2id$x" {
			t.Fatalf("unmoved %+v %v", un, err)
		}
		old, err := s.OldSessions(ctx, id, now)
		if err != nil || len(old) != 1 || old[0].ID != "live" || !old[0].ExpiresAt.Equal(now.Add(time.Minute)) {
			t.Fatalf("old sessions %+v %v", old, err)
		}
		if err := s.LinkAuthUser(ctx, id, authOld); err != nil {
			t.Fatal(err)
		}
		if err := s.LinkAuthUser(ctx, id, authOther); !errors.Is(err, ErrConflict) {
			t.Fatalf("second link: %v", err)
		}
		if un, _ := s.UnmovedUsers(ctx); len(un) != 0 {
			t.Fatalf("still unmoved: %+v", un)
		}
		if row, err := s.r.users.Get(ctx, id); err != nil || row.PasswordHash != "" {
			t.Fatalf("hash left: %+v %v", row, err)
		}
		if n, err := s.r.sessions.Count(ctx, ablage.All); err != nil || n != 0 {
			t.Fatalf("%d sessions left (%v)", n, err)
		}
		u, err := s.UserByAuthID(ctx, authOld)
		if err != nil || u.ID != id || u.AuthUserID != authOld {
			t.Fatalf("by auth id %+v %v", u, err)
		}
		if _, err := s.UserByAuthID(ctx, ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("empty auth id: %v", err)
		}
		ids, err := s.AuthUserIDs(ctx)
		if err != nil || len(ids) != 2 || !ids[authOld] || !ids[authNew] {
			t.Fatalf("auth ids %v %v", ids, err)
		}
	})
}
