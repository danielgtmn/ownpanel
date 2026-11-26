package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// dumpTables returns every row of the tables, as text, for comparing.
func dumpTables(t *testing.T, file string, tables ...string) map[string][]string {
	t.Helper()
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	out := map[string][]string{}
	for _, table := range tables {
		rows, err := db.Query(`SELECT * FROM "` + table + `" ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			var b strings.Builder
			for i, v := range vals {
				if i > 0 {
					b.WriteString("|")
				}
				b.WriteString(fmtValue(v))
			}
			out[table] = append(out[table], b.String())
		}
		rows.Close()
	}
	return out
}

func fmtValue(v any) string {
	switch v := v.(type) {
	case nil:
		return "NULL"
	case []byte:
		return string(v)
	}
	return fmt.Sprint(v)
}

var authTables = []string{"auth_users", "auth_sessions", "auth_accounts", "auth_verifications", "auth_rate_limits"}

// indexes returns the names of the indexes on table.
func indexes(t *testing.T, file, table string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND name NOT LIKE 'sqlite_%' ORDER BY name`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		out = append(out, n)
	}
	return out
}

func hasRef(t *testing.T, file string) bool {
	t.Helper()
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ok, err := hasAuthRef(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// The conversion leaves sesam's tables exactly as sesam 1.x wrote them;
// only their indexes get ablage's names (same columns, same uniqueness),
// and users reference auth_users from the start.
func TestConvertLegacyKeepsSignIn(t *testing.T) {
	file := legacyDB(t)
	before := dumpTables(t, file, authTables...)
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if after := dumpTables(t, file, authTables...); !reflect.DeepEqual(after, before) {
		t.Fatalf("sesam's rows changed\nbefore %v\n after %v", before, after)
	}
	if len(before["auth_users"]) != 3 || len(before["auth_sessions"]) != 1 || len(before["auth_rate_limits"]) != 5 {
		t.Fatalf("test data: %v", before)
	}
	for _, table := range authTables {
		for _, ix := range indexes(t, file, table) {
			if !managedName(ix) {
				t.Errorf("index %s of sesam 1.x left", ix)
			}
		}
	}
	if got := indexes(t, file, "auth_users"); len(got) != 3 {
		t.Errorf("auth_users indexes %v", got)
	}
	if !hasRef(t, file) {
		t.Fatal("users do not reference auth_users")
	}
	if b := backups(t, file); len(b) != 1 {
		t.Fatalf("copies %v", b)
	}
	if b, _ := filepath.Glob(file + ".pre-auth-ref-*"); len(b) != 0 {
		t.Fatalf("a converted database needs no second step: %v", b)
	}
}

func managedName(name string) bool {
	// ablage's index names end in _<8 hex digits>_key or _idx.
	i := strings.LastIndex(name, "_")
	if i < 9 {
		return false
	}
	h := name[i-8 : i]
	for _, c := range h {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return name[i-9] == '_' && (name[i+1:] == "key" || name[i+1:] == "idx")
}

// earlyAblageDB is a database as the first ablage build of the panel left
// it: converted, but users.auth_user_id without the reference to
// auth_users. One user points at a sign-in user that does not exist.
func earlyAblageDB(t *testing.T) string {
	t.Helper()
	file := legacyDB(t)
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var create string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'users'`).Scan(&create); err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(create, ` REFERENCES "auth_users" ("id")`, "", 1)
	if old == create {
		t.Fatalf("no reference in %s", create)
	}
	old = strings.Replace(old, `CREATE TABLE "users"`, `CREATE TABLE "users_old"`, 1)
	var idx []string
	rows, _ := db.Query(`SELECT sql FROM sqlite_master WHERE type = 'index' AND tbl_name = 'users' AND sql IS NOT NULL`)
	for rows.Next() {
		var q string
		rows.Scan(&q)
		idx = append(idx, q)
	}
	rows.Close()
	for _, q := range append([]string{
		`PRAGMA foreign_keys = OFF`,
		old,
		`INSERT INTO users_old SELECT * FROM users`,
		`DROP TABLE users`,
		`ALTER TABLE users_old RENAME TO users`,
	}, append(idx,
		`UPDATE users SET auth_user_id = 'gone' WHERE name = 'jürgen'`,
		`DELETE FROM store_format WHERE id <> 'ablage'`,
	)...) {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return file
}

// A database converted without the reference gets it once: the users table
// is rebuilt in place, nothing that references users is lost, and a link
// to a missing sign-in user is cleared.
func TestLinkAuthUsers(t *testing.T) {
	file := earlyAblageDB(t)
	if hasRef(t, file) {
		t.Fatal("the test database has the reference already")
	}
	keep := []string{"tokens", "sites", "dns_connections", "domains", "redirects", "mail_domains", "organisations", "sessions"}
	before := dumpTables(t, file, append(keep, authTables...)...)
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRef(t, file) {
		t.Fatal("no reference after Open")
	}
	b, _ := filepath.Glob(file + ".pre-auth-ref-*")
	if len(b) != 1 {
		t.Fatalf("copies %v", b)
	}
	if after := dumpTables(t, file, append(keep, authTables...)...); !reflect.DeepEqual(after, before) {
		t.Fatalf("rows changed\nbefore %v\n after %v", before, after)
	}
	users, err := s.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []User{
		{ID: "10", Name: "bob", Role: RoleMember, AuthUserID: authBob, CreatedAt: at(1700000000)},
		{ID: "11", Name: "jürgen", Role: RoleMember, CreatedAt: at(1700000005)},
		{ID: "9", Name: "root", Role: RoleAdmin, AuthUserID: authRoot, CreatedAt: at(1700000000)},
	}
	if !reflect.DeepEqual(users, want) {
		t.Fatalf("users\n got %+v\nwant %+v", users, want)
	}
	// The unique indexes are back.
	if _, err := s.CreateUser(ctx, "bob", "", RoleMember, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	if err := s.LinkAuthUser(ctx, "11", authBob); err == nil {
		t.Fatal("two users linked to one sign-in user")
	}
	// References work again on the connection.
	if _, err := s.CreateUser(ctx, "ghost", "no-such-sign-in", RoleMember, false); err == nil {
		t.Fatal("dangling link accepted")
	}
	// The other tables still reference the users: deleting one takes its
	// tokens along.
	carol, err := s.CreateUser(ctx, "carol", "", RoleMember, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateToken(ctx, Token{UserID: carol.ID, Name: "ci", Hash: "hash-carol", Access: "read"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, carol.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.TokenByHash(ctx, "hash-carol"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tokens of a deleted user: %v", err)
	}
	if _, err := s.CreateToken(ctx, Token{UserID: "no-such-user", Name: "ci", Hash: "hash-x", Access: "read"}); err == nil {
		t.Fatal("token of a missing user accepted")
	}
	s.Close()

	// The next start changes nothing.
	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if b, _ := filepath.Glob(file + ".pre-auth-ref-*"); len(b) != 1 {
		t.Fatalf("copies %v", b)
	}
}

// A rebuild that fails on the way leaves the database as it was.
func TestLinkAuthUsersFailureChangesNothing(t *testing.T) {
	file := earlyAblageDB(t)
	all := append([]string{"users", "tokens", "sites"}, authTables...)
	before := dumpTables(t, file, all...)
	authRefHook = func() error { return errors.New("power cut") }
	_, err := Open(file)
	authRefHook = func() error { return nil }
	if err == nil || !strings.Contains(err.Error(), "power cut") {
		t.Fatalf("open: %v", err)
	}
	if hasRef(t, file) {
		t.Fatal("half done")
	}
	if after := dumpTables(t, file, all...); !reflect.DeepEqual(after, before) {
		t.Fatalf("rows changed\nbefore %v\n after %v", before, after)
	}
	if got := tables(t, file); contains(got, "users_new") {
		t.Fatalf("tables %v", got)
	}
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !hasRef(t, file) {
		t.Fatal("no reference after the retry")
	}
}
