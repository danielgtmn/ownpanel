package store

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ablage"
)

// The sign-in users of testdata/sesam_v1.sql.
const (
	authRoot = "v1-signin-root"
	authBob  = "v1-signin-bob"
)

// legacyDB writes a database the way the panel kept it before ablage: the
// old migrations, sesam's tables as sesam 1.x wrote them
// (testdata/sesam_v1.sql), then the rows of testdata/legacy_rows.sql.
func legacyDB(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "ownpanel.db")
	db, err := sql.Open("sqlite", file+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	sub, _ := fs.Sub(legacyFiles, "legacy")
	if err := migrate(db, sub); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"testdata/sesam_v1.sql", "testdata/legacy_rows.sql"} {
		rows, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(rows)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	return file
}

func backups(t *testing.T, file string) []string {
	t.Helper()
	list, err := filepath.Glob(file + ".pre-ablage-*")
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func tables(t *testing.T, file string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
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

func at(sec int64) time.Time { return time.Unix(sec, 0).UTC() }

func atPtr(sec int64) *time.Time { v := at(sec); return &v }

func TestConvertLegacy(t *testing.T) {
	file := legacyDB(t)
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// The copy from before: the old schema, private.
	b := backups(t, file)
	if len(b) != 1 {
		t.Fatalf("backups %v", b)
	}
	if fi, err := os.Stat(b[0]); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup %v %v", fi, err)
	}
	if got := tables(t, b[0]); !contains(got, "schema_migrations") || !contains(got, "login_attempts") {
		t.Fatalf("backup tables %v", got)
	}
	// The old tables and the version table are gone, sesam's table stays.
	got := tables(t, file)
	for _, name := range got {
		if strings.HasPrefix(name, "legacy_") || name == "schema_migrations" || name == "login_attempts" {
			t.Errorf("left over: %s", name)
		}
	}
	if n, err := s.st.Count(ctx, "auth_users", ablage.All); err != nil || n != 3 {
		t.Fatalf("sesam's rows: %d %v", n, err)
	}

	// Users: ids as text, links to sesam kept, the unmoved one with hash.
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
	if u, err := s.UserByAuthID(ctx, authRoot); err != nil || u.ID != "9" {
		t.Fatalf("by auth id %+v %v", u, err)
	}
	un, _ := s.UnmovedUsers(ctx)
	if len(un) != 1 || un[0].ID != "11" || un[0].PasswordHash != "kept-hash-of-juergen" {
		t.Fatalf("unmoved %+v", un)
	}
	old, _ := s.OldSessions(ctx, "11", at(1700000000))
	if len(old) != 1 || old[0] != (OldSession{ID: "a1b2", CreatedAt: at(1700000010), ExpiresAt: at(4000000000)}) {
		t.Fatalf("old sessions %+v", old)
	}

	// Tokens: NULL sites stay nil, JSON kept, times.
	tok, u, err := s.TokenByHash(ctx, "hash-2")
	wantTok := Token{ID: "2", UserID: "10", Name: "deploy ✓", Hash: "hash-2", Access: "read", Sites: []string{"bobsite", "other"},
		CreatedAt: at(1700000100), LastUsedAt: atPtr(1700000200), ExpiresAt: atPtr(1800000000)}
	if err != nil || u.Name != "bob" || !reflect.DeepEqual(tok, wantTok) {
		t.Fatalf("token\n got %+v %v\nwant %+v", tok, err, wantTok)
	}
	if tok, _, _ := s.TokenByHash(ctx, "hash-1"); tok.Sites != nil || tok.LastUsedAt != nil || tok.ExpiresAt != nil || tok.ID != "1" {
		t.Fatalf("token 1 %+v", tok)
	}

	// Sites and domains; the ownerless site stays ownerless.
	sites, _ := s.Sites(ctx)
	wantSites := []Site{
		{Name: "bobsite", Domains: []string{"bob.example.com", "www.bob.example.com"}, OwnerID: "10", Owner: "bob"},
		{Name: "orphan", Domains: []string{"orphan.example.org"}},
	}
	if !reflect.DeepEqual(sites, wantSites) {
		t.Fatalf("sites %+v", sites)
	}

	// The activity log keeps its order (by the former id, not by time).
	acts, _ := s.Activities(ctx, 10)
	if len(acts) != 3 || acts[0].ID != "11" || acts[0].Detail != "v1 — ünïcode" || acts[1].ID != "10" || acts[2].ID != "9" ||
		!acts[0].At.Equal(at(1700000399)) {
		t.Fatalf("activities %+v", acts)
	}
	if err := s.AddActivity(ctx, Activity{Actor: "root", Action: "after"}); err != nil {
		t.Fatal(err)
	}
	if acts, _ := s.Activities(ctx, 1); acts[0].Action != "after" {
		t.Fatalf("new entry not first: %+v", acts)
	}

	settings, _ := s.Settings(ctx)
	if !reflect.DeepEqual(settings, map[string]string{"panel_domain": "panel.example.com", "mail_app_password_key": "sealed-value-placeholder"}) {
		t.Fatalf("settings %v", settings)
	}

	// DNS connections: secrets byte for byte, unknown stays nil.
	conns, _ := s.DNSConnections(ctx, "")
	if len(conns) != 3 {
		t.Fatalf("connections %+v", conns)
	}
	c3, _ := s.DNSConnection(ctx, "3")
	if !reflect.DeepEqual(c3.Secret, []byte{0x00, 0xff, 0x10, 0xa0}) || c3.CanWrite != nil || c3.ZoneCount != nil || c3.LastCheckedAt != nil ||
		c3.Owner != "bob" || c3.Name != "Bob's project" {
		t.Fatalf("connection 3 %+v", c3)
	}
	c4, _ := s.DNSConnection(ctx, "4")
	if c4.CanWrite == nil || *c4.CanWrite || c4.ZoneCount == nil || *c4.ZoneCount != 5 || !c4.LastCheckedAt.Equal(at(1700000600)) || c4.LastError != "rate limited" {
		t.Fatalf("connection 4 %+v", c4)
	}
	c5, _ := s.DNSConnection(ctx, "5")
	if c5.CanWrite == nil || !*c5.CanWrite || *c5.ZoneCount != 0 || len(c5.Secret) != 0 {
		t.Fatalf("connection 5 %+v", c5)
	}
	zones, _ := s.AdoptedDNSZones(ctx, "10")
	if len(zones) != 1 || zones[0] != (AdoptedDNSZone{ConnectionID: "3", Zone: "bob.example.com", CreatedAt: at(1700000700)}) {
		t.Fatalf("zones %+v", zones)
	}
	if created, err := s.AdoptDNSZone(ctx, "3", "bob.example.com"); err != nil || created {
		t.Fatalf("adopted zone must still be known: %v %v", created, err)
	}
	domains, _ := s.Domains(ctx, "10")
	if len(domains) != 1 || domains[0].Domain != "elsewhere.example" {
		t.Fatalf("domains %+v", domains)
	}
	if created, _ := s.AddDomain(ctx, "10", "elsewhere.example"); created {
		t.Fatal("domain must still be known")
	}

	// Redirects: the path redirect belongs to the site's owner.
	reds, _ := s.Redirects(ctx)
	wantReds := []Redirect{
		{ID: "8", OwnerID: "10", Owner: "bob", SiteName: "bobsite", SourceDomain: "bob.example.com", SourcePath: "/blog/*", Target: "/news",
			Code: 308, KeepQuery: true, CreatedAt: at(1700000901)},
		{ID: "7", OwnerID: "9", Owner: "root", SourceDomain: "old.example.com", Target: "https://new.example.com", Code: 301, KeepPath: true,
			CreatedAt: at(1700000900)},
	}
	if !reflect.DeepEqual(reds, wantReds) {
		t.Fatalf("redirects\n got %+v\nwant %+v", reds, wantReds)
	}

	// Mail.
	md, _ := s.MailDomains(ctx)
	wantMD := []MailDomain{
		{Domain: "bob.example.com", OwnerID: "10", Owner: "bob", Active: true, ActivatedAt: atPtr(1700001210), CheckedAt: atPtr(1700001200),
			CreatedAt: at(1700001150), Mailboxes: 2, Aliases: 2, OrganisationID: "org-1a2b", OrganisationName: "Bob & Co", OrganisationOwnerID: "10"},
		{Domain: "example.net", OwnerID: "9", Owner: "root", CheckError: "no MX record", CreatedAt: at(1700001151)},
	}
	if !reflect.DeepEqual(md, wantMD) {
		t.Fatalf("mail domains\n got %+v\nwant %+v", md, wantMD)
	}
	keys, _ := s.DKIMKeys(ctx, "bob.example.com")
	if len(keys) != 2 || keys[0].Selector != "s1" || keys[1].State != DKIMActive || !reflect.DeepEqual(keys[0].PrivateKey, []byte{0, 1, 2, 3, 4}) ||
		!keys[1].CreatedAt.Equal(at(1700001300)) {
		t.Fatalf("keys %+v", keys)
	}
	// One active key per domain still holds.
	if err := s.r.dkimKeys.Create(ctx, &dkimKeyRow{Domain: "bob.example.com", Selector: "s3", State: DKIMActive, ActiveDomain: ptr("bob.example.com")}); !errors.Is(err, ablage.ErrDuplicate) {
		t.Fatalf("second active key: %v", err)
	}
	boxes, _ := s.Mailboxes(ctx, "bob.example.com")
	wantBoxes := []Mailbox{
		{ID: "9", StorageID: "storage09", Domain: "bob.example.com", LocalPart: "info", PasswordHash: "$argon2id$box", SendPerHour: 100, SendPerDay: 1000,
			CreatedAt: at(1700001400), OwnerID: "10", Owner: "bob", DisplayName: "Bob Ünïcode", AppPasswordsOnly: true, Revision: 42,
			AdminDisplayNameAt: at(1700001500), Kind: "personal"},
		{ID: "10", StorageID: "storage10", Domain: "bob.example.com", LocalPart: "team", QuotaBytes: 5368709120,
			CreatedAt: at(1700001400), OwnerID: "10", Owner: "bob", AdminPasswordAt: at(1700001600), AdminAppPasswordsOnlyAt: at(1700001601),
			Kind: "shared", KeepSentCopy: true, AdminGrantsAt: at(1700001602)},
	}
	if len(boxes) != 2 {
		t.Fatalf("mailboxes %+v", boxes)
	}
	for i := range boxes {
		if !sameMailbox(boxes[i], wantBoxes[i]) {
			t.Fatalf("mailbox %d\n got %+v\nwant %+v", i, boxes[i], wantBoxes[i])
		}
	}
	if !boxes[0].AdminPasswordAt.IsZero() || !boxes[0].AdminGrantsAt.IsZero() {
		t.Fatalf("0 must stay never: %+v", boxes[0])
	}
	aliases, _ := s.MailAliases(ctx, "")
	if len(aliases) != 2 || aliases[0].LocalPart != "*" || !reflect.DeepEqual(aliases[1].Targets, []string{"info@bob.example.com", "extern@example.org"}) ||
		aliases[1].SenderPolicy != "members" || !aliases[1].HideFromDirectory || aliases[1].ID != "6" || aliases[1].Owner != "bob" {
		t.Fatalf("aliases %+v", aliases)
	}
	if p, _ := s.PendingMailDeletes(ctx); !reflect.DeepEqual(p, []string{"gone01"}) {
		t.Fatalf("pending %v", p)
	}
	aps, _ := s.AppPasswords(ctx, "9")
	if len(aps) != 2 || aps[0].LastUsedAt != nil || aps[1].Name != "Phone 📱" || !aps[1].LastUsedAt.Equal(at(1700002000)) || aps[1].LastUsedNet != "203.0.113.0/24" {
		t.Fatalf("app passwords %+v", aps)
	}
	grants, _ := s.MailboxGrants(ctx, "10", "")
	if len(grants) != 1 || grants[0].GranteeID != "9" || !reflect.DeepEqual(grants[0].Rights, []string{"read", "send_as"}) || grants[0].GranteeAddress != "info@bob.example.com" {
		t.Fatalf("grants %+v", grants)
	}
	if existed, err := s.SetMailboxGrant(ctx, "10", "9", []string{"read"}, 0); err != nil || !existed {
		t.Fatalf("grant must still be known: %v %v", existed, err)
	}
	if nf, _ := s.MailboxesNoForwarding(ctx); !reflect.DeepEqual(nf, map[string]bool{"10": true}) {
		t.Fatalf("no forwarding %v", nf)
	}
	orgs, _ := s.Organisations(ctx)
	if len(orgs) != 1 || orgs[0].Owner != "bob" || !reflect.DeepEqual(orgs[0].Domains, []string{"bob.example.com"}) {
		t.Fatalf("organisations %+v", orgs)
	}

	// References still hold: deleting bob is refused while he owns sites.
	if err := s.DeleteUser(ctx, "10"); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete owner: %v", err)
	}
	// New rows get ablage ids next to the converted ones.
	nu, err := s.CreateUser(ctx, "new", "", RoleMember, false)
	if err != nil || len(nu.ID) != 26 {
		t.Fatalf("new user %+v %v", nu, err)
	}

	// A second start changes nothing and makes no second copy.
	s.Close()
	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if b := backups(t, file); len(b) != 1 {
		t.Fatalf("second start made a copy: %v", b)
	}
	if again, _ := s.Users(ctx); len(again) != 4 {
		t.Fatalf("users after reopen %+v", again)
	}
}

func sameMailbox(a, b Mailbox) bool {
	for _, p := range [][2]*time.Time{{&a.AdminPasswordAt, &b.AdminPasswordAt}, {&a.AdminDisplayNameAt, &b.AdminDisplayNameAt},
		{&a.AdminAppPasswordsOnlyAt, &b.AdminAppPasswordsOnlyAt}, {&a.AdminGrantsAt, &b.AdminGrantsAt}, {&a.CreatedAt, &b.CreatedAt}} {
		if !p[0].Equal(*p[1]) {
			return false
		}
		*p[0], *p[1] = time.Time{}, time.Time{}
	}
	return reflect.DeepEqual(a, b)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// A conversion that fails on the way leaves the database as it was; the
// next start tries again.
func TestConvertLegacyFailureChangesNothing(t *testing.T) {
	file := legacyDB(t)
	before := tables(t, file)
	legacyHook = func(step string) error { return errors.New("power cut") }
	_, err := Open(file)
	legacyHook = func(string) error { return nil }
	if err == nil || !strings.Contains(err.Error(), "power cut") {
		t.Fatalf("open: %v", err)
	}
	if after := tables(t, file); !reflect.DeepEqual(after, before) {
		t.Fatalf("tables changed\nbefore %v\n after %v", before, after)
	}
	db, _ := sql.Open("sqlite", file)
	var id any
	db.QueryRow(`SELECT id FROM users WHERE name = 'root'`).Scan(&id)
	db.Close()
	if id != int64(9) {
		t.Fatalf("users changed: id %#v", id)
	}
	if len(backups(t, file)) != 1 {
		t.Fatal("the copy must stay")
	}
	// The retry makes a copy of its own.
	defer func() {
		if n := len(backups(t, file)); n != 2 {
			t.Errorf("%d copies", n)
		}
	}()
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if u, err := s.User(ctx, "9"); err != nil || u.Name != "root" {
		t.Fatalf("after retry %+v %v", u, err)
	}
}

// A new database never takes the conversion path.
func TestFreshDatabaseIsNotConverted(t *testing.T) {
	s, dir := open(t)
	file := filepath.Join(dir, "ownpanel.db")
	if b := backups(t, file); len(b) != 0 {
		t.Fatalf("copies %v", b)
	}
	if got := tables(t, file); contains(got, "schema_migrations") || !contains(got, "store_format") {
		t.Fatalf("tables %v", got)
	}
	if ok, err := s.r.format.Exists(ctx, formatAblage); err != nil || !ok {
		t.Fatalf("format %v %v", ok, err)
	}
}

// The conversion creates the tables as Migrate would: Migrate afterwards
// has nothing to do, so the declared and the created schema agree.
func TestConvertedSchemaMatchesDeclared(t *testing.T) {
	file := legacyDB(t)
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	fresh, dir := open(t)
	_ = fresh
	schema := func(file string) map[string]string {
		db, err := sql.Open("sqlite", file)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		rows, err := db.Query(`SELECT name, coalesce(sql, '') FROM sqlite_master WHERE name NOT LIKE 'auth_%' AND name NOT LIKE 'sqlite_%'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var n, q string
			rows.Scan(&n, &q)
			out[n] = q
		}
		return out
	}
	got, want := schema(file), schema(filepath.Join(dir, "ownpanel.db"))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("schema differs\n got %v\nwant %v", got, want)
	}
}
