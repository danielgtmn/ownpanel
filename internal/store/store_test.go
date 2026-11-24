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
)

var ctx = context.Background()

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
	if _, err := s.CreateSite(ctx, "blog", []string{"blog.example.com"}, 0); err != nil {
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
	s, _ := open(t)
	admin, err := s.CreateUser(ctx, "admin", "h", RoleAdmin, true)
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
}

func TestUsers(t *testing.T) {
	s, _ := open(t)
	if _, err := s.CreateSite(ctx, "old", nil, 0); err != nil {
		t.Fatal(err)
	}
	admin, err := s.CreateUser(ctx, "admin", "h", RoleAdmin, true)
	if err != nil {
		t.Fatal(err)
	}
	if site, _ := s.Site(ctx, "old"); site.OwnerID != admin.ID {
		t.Fatalf("first admin must own ownerless sites: %+v", site)
	}
	if _, err := s.CreateUser(ctx, "second", "h", RoleAdmin, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("firstOnly with users present: %v", err)
	}
	if _, err := s.CreateUser(ctx, "admin", "h", RoleMember, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	member, err := s.CreateUser(ctx, "bob", "h", RoleMember, false)
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
	if err := s.CreateSession(ctx, "sess", member.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, member.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.TokenByHash(ctx, "bobhash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("token of deleted user: %v", err)
	}
	if _, err := s.SessionUser(ctx, "sess", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session of deleted user: %v", err)
	}

	// With a second admin the first can step down.
	other, _ := s.CreateUser(ctx, "carol", "h", RoleAdmin, false)
	if err := s.SetRole(ctx, admin.ID, RoleMember); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRole(ctx, other.ID, RoleMember); !errors.Is(err, ErrConflict) {
		t.Fatalf("now carol is the last admin: %v", err)
	}
}

func TestSessionsAndTokens(t *testing.T) {
	s, _ := open(t)
	u, _ := s.CreateUser(ctx, "admin", "h", RoleAdmin, true)
	now := time.Unix(1_800_000_000, 0)
	s.CreateSession(ctx, "a", u.ID, now.Add(time.Hour))
	s.CreateSession(ctx, "b", u.ID, now.Add(time.Hour))
	s.CreateSession(ctx, "old", u.ID, now.Add(-time.Second))

	if _, err := s.SessionUser(ctx, "a", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, "a", now.Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session: %v", err)
	}
	if err := s.DeleteExpiredSessions(ctx, now); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&n)
	if n != 2 {
		t.Fatalf("%d sessions left", n)
	}
	if err := s.SetPassword(ctx, u.ID, "h2", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, "b", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("password change must end the other sessions")
	}
	if got, err := s.SessionUser(ctx, "a", now); err != nil || got.PasswordHash != "h2" {
		t.Fatalf("kept session: %+v %v", got, err)
	}

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
	if err := s.DeleteToken(ctx, u.ID+1, tok.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user's token: %v", err)
	}
	if err := s.DeleteToken(ctx, u.ID, tok.ID); err != nil {
		t.Fatal(err)
	}
}

func TestImportStateFile(t *testing.T) {
	s, dir := open(t)
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
	if err != nil || site.OwnerID != 0 || site.Domains[0] != "blog.example.com" {
		t.Fatalf("%+v %v", site, err)
	}
	admin, _ := s.CreateUser(ctx, "admin", "h", RoleAdmin, true)
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
}

func TestImportOwnedByExistingAdmin(t *testing.T) {
	s, dir := open(t)
	admin, _ := s.CreateUser(ctx, "admin", "h", RoleAdmin, true)
	file := filepath.Join(dir, "state.json")
	os.WriteFile(file, []byte(`[{"name":"blog","domains":["blog.example.com"]}]`), 0o600)
	if _, err := s.ImportStateFile(ctx, file); err != nil {
		t.Fatal(err)
	}
	if site, _ := s.Site(ctx, "blog"); site.OwnerID != admin.ID {
		t.Fatalf("%+v", site)
	}
}

func TestVacuumInto(t *testing.T) {
	s, dir := open(t)
	s.CreateSite(ctx, "blog", []string{"blog.example.com"}, 0)
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
	s, _ := open(t)
	for _, a := range []string{"one", "two", "three"} {
		if err := s.AddActivity(ctx, Activity{Actor: "admin", Action: a}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Activities(ctx, 2)
	if err != nil || len(got) != 2 || got[0].Action != "three" || got[1].Action != "two" {
		t.Fatalf("%+v %v", got, err)
	}
}
