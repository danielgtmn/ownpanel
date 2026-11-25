package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/store"
)

func activityText(t *testing.T, w *mailWorld) string {
	t.Helper()
	acts, err := w.svc.store.Activities(context.Background(), 500)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(acts)
	return string(b)
}

func lastActivity(t *testing.T, w *mailWorld) (action, target, detail, actor string) {
	t.Helper()
	acts, err := w.svc.store.Activities(context.Background(), 1)
	if err != nil || len(acts) == 0 {
		t.Fatal(err)
	}
	return acts[0].Action, acts[0].Target, acts[0].Detail, acts[0].Actor
}

func pushedDomain(t *testing.T, cfg *config.Config, name string) config.Domain {
	t.Helper()
	for _, d := range cfg.Domains {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("domain %s not pushed", name)
	return config.Domain{}
}

func pushedMailbox(t *testing.T, cfg *config.Config, addr string) config.Mailbox {
	t.Helper()
	for _, m := range cfg.Mailboxes {
		if m.Address == addr {
			return m
		}
	}
	t.Fatalf("mailbox %s not pushed", addr)
	return config.Mailbox{}
}

func TestOrganisations(t *testing.T) {
	ctx := context.Background()
	w := newMailWorld(t)
	svc := w.svc
	w.mx.point("bobother.example.com", testMailHost)
	if _, err := svc.EnableMailDomain(ctx, bob, "bobother.example.com"); err != nil {
		t.Fatal(err)
	}

	// Standalone domains are pushed without an organisation: the mail
	// service treats each as its own.
	if err := svc.mailSync(ctx); err != nil {
		t.Fatal(err)
	}
	if o := pushedDomain(t, w.mail.last(), "bobsite.example.com").Organisation; o != "" {
		t.Fatalf("standalone %q", o)
	}

	// A member groups their own domains.
	org, err := svc.CreateOrganisation(ctx, bob, "  Bob   GmbH ")
	if err != nil || org.Name != "Bob GmbH" || !strings.HasPrefix(org.ID, "org-") || org.Owner != "bob" {
		t.Fatalf("create %+v %v", org, err)
	}
	for _, d := range []string{"bobsite.example.com", "bobother.example.com"} {
		md, err := svc.SetMailDomainOrganisation(ctx, bob, d, org.ID)
		if err != nil || md.Organisation == nil || md.Organisation.ID != org.ID || !md.Organisation.Own {
			t.Fatalf("group %s: %+v %v", d, md, err)
		}
	}
	cfg := w.mail.last()
	a, b := pushedDomain(t, cfg, "bobsite.example.com").Organisation, pushedDomain(t, cfg, "bobother.example.com").Organisation
	if a != org.ID || b != org.ID || strings.Contains(a, "bob") {
		t.Fatalf("pushed %q %q", a, b)
	}
	md, _ := svc.MailDomainOf(ctx, bob, "bobsite.example.com")
	if len(md.Organisation.Domains) != 1 || md.Organisation.Domains[0] != "bobother.example.com" {
		t.Fatalf("view %+v", md.Organisation)
	}
	if act, target, detail, _ := lastActivity(t, w); act != "mail.domain.organisation" || target != "bobother.example.com" ||
		detail != "into Bob GmbH with bobsite.example.com" {
		t.Fatalf("activity %s %s %q", act, target, detail)
	}

	// Organisations of others are invisible to members.
	rootOrg, err := svc.CreateOrganisation(ctx, admin, "Root Ltd")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetMailDomainOrganisation(ctx, bob, "bobsite.example.com", rootOrg.ID); KindOf(err) != NotFound {
		t.Fatalf("member into admin's organisation: %v", err)
	}
	if _, err := svc.RenameOrganisation(ctx, bob, rootOrg.ID, "x"); KindOf(err) != NotFound {
		t.Fatalf("rename others': %v", err)
	}
	if err := svc.DeleteOrganisation(ctx, bob, rootOrg.ID); KindOf(err) != NotFound {
		t.Fatalf("delete others': %v", err)
	}
	if list, _ := svc.Organisations(ctx, bob); len(list) != 1 || list[0].ID != org.ID {
		t.Fatalf("bob sees %+v", list)
	}
	if list, _ := svc.Organisations(ctx, admin); len(list) != 2 {
		t.Fatalf("admin sees %+v", list)
	}
	// Read-only and site tokens change nothing.
	for _, a := range []Actor{bobRead, bobLimited} {
		if _, err := svc.CreateOrganisation(ctx, a, "x"); KindOf(err) != Forbidden {
			t.Errorf("%s create: %v", a.Name, err)
		}
		if _, err := svc.SetMailDomainOrganisation(ctx, a, "bobsite.example.com", ""); KindOf(err) != Forbidden {
			t.Errorf("%s set: %v", a.Name, err)
		}
	}

	// Grouping domains of different owners is for admins only, and logged
	// as such.
	md, err = svc.SetMailDomainOrganisation(ctx, admin, "rootsite.example.com", org.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, detail, _ := lastActivity(t, w); !strings.Contains(detail, "(domains of different owners)") {
		t.Fatalf("cross-owner not logged: %q", detail)
	}
	// Now bob's organisation holds root's domain: bob may not add more of
	// his domains to it, but may take his own out.
	if _, err := svc.SetMailDomainOrganisation(ctx, bob, "bobother.example.com", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetMailDomainOrganisation(ctx, bob, "bobother.example.com", org.ID); KindOf(err) != Forbidden {
		t.Fatalf("member groups with another owner's domain: %v", err)
	}
	// Bob sees only his own domains of it.
	md, _ = svc.MailDomainOf(ctx, bob, "bobsite.example.com")
	if len(md.Organisation.Domains) != 0 {
		t.Fatalf("bob sees %v", md.Organisation.Domains)
	}
	// A member's domain the admin put into the admin's organisation shows
	// it as not their own.
	if _, err := svc.SetMailDomainOrganisation(ctx, admin, "bobother.example.com", rootOrg.ID); err != nil {
		t.Fatal(err)
	}
	md, _ = svc.MailDomainOf(ctx, bob, "bobother.example.com")
	if md.Organisation == nil || md.Organisation.Own || md.Organisation.Name != "Root Ltd" {
		t.Fatalf("admin's organisation as bob sees it: %+v", md.Organisation)
	}

	// Deleting an organisation lets its domains stand alone again.
	if err := svc.DeleteOrganisation(ctx, bob, org.ID); err != nil {
		t.Fatal(err)
	}
	cfg = w.mail.last()
	if o := pushedDomain(t, cfg, "bobsite.example.com").Organisation; o != "" {
		t.Fatalf("after delete %q", o)
	}
	if _, err := svc.CreateOrganisation(ctx, bob, "a\nb"); KindOf(err) != Invalid {
		t.Fatalf("bad name: %v", err)
	}
}

func TestDisplayNameAndAppPasswordsOnly(t *testing.T) {
	ctx := context.Background()
	w := newMailWorld(t)
	svc := w.svc
	on := true
	m, err := svc.SetMailboxAccount(ctx, bob, w.bobBox, MailboxAccount{DisplayName: strp(" Info  Team "), AppPasswordsOnly: &on})
	if err != nil || m.DisplayName != "Info Team" || !m.AppPasswordsOnly {
		t.Fatalf("%+v %v", m, err)
	}
	pm := pushedMailbox(t, w.mail.last(), "info@bobsite.example.com")
	if pm.DisplayName != "Info Team" || !pm.AppPasswordsOnly {
		t.Fatalf("pushed %+v", pm)
	}
	for _, bad := range []string{"a\r\nBcc: x@example.org", "<x>", `"q"`, strings.Repeat("x", 65)} {
		if _, err := svc.SetMailboxAccount(ctx, bob, w.bobBox, MailboxAccount{DisplayName: &bad}); KindOf(err) != Invalid {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := svc.SetMailboxAccount(ctx, bob, w.rootBox, MailboxAccount{DisplayName: strp("x")}); KindOf(err) != NotFound {
		t.Fatalf("other's mailbox: %v", err)
	}
	if _, err := svc.SetMailboxAccount(ctx, bobRead, w.bobBox, MailboxAccount{DisplayName: strp("x")}); KindOf(err) != Forbidden {
		t.Fatalf("read token: %v", err)
	}
	created, err := svc.CreateMailbox(ctx, bob, "bobsite.example.com", MailboxInput{LocalPart: "anna", Password: "mailbox-password-2", DisplayName: "Anna"})
	if err != nil || created.DisplayName != "Anna" {
		t.Fatalf("create %+v %v", created, err)
	}
	// A main password that looks like an app password is refused: mail
	// programs would only ever check it as an app password.
	_, _, token := config.NewAppPassword()
	if err := svc.SetMailboxPassword(ctx, bob, w.bobBox, token); KindOf(err) != Invalid {
		t.Fatalf("app-password-like password: %v", err)
	}
}

func TestAppPasswords(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(old)

	w := newMailWorld(t)
	svc := w.svc
	ap, err := svc.CreateAppPassword(ctx, bob, w.bobBox, " Phone ")
	if err != nil || ap.Name != "Phone" || !config.LooksLikeAppPassword(ap.Password) || ap.LastUsedAt != nil {
		t.Fatalf("create %+v %v", ap, err)
	}
	id, secret, _ := config.ParseAppPassword(ap.Password)
	if id != ap.ID {
		t.Fatal("id")
	}
	// The mail service gets the key and the MAC, which checks the token.
	cfg := w.mail.last()
	pm := pushedMailbox(t, cfg, "info@bobsite.example.com")
	if len(pm.AppPasswords) != 1 || !config.CheckAppPassword(cfg.AppPasswordKey, pm.StorageID, pm.AppPasswords[0], secret) {
		t.Fatalf("pushed %+v", pm.AppPasswords)
	}
	// The key stays the same across pushes and restarts of the panel.
	svc.appKey = ""
	if k, err := svc.appPasswordKey(ctx); err != nil || k != cfg.AppPasswordKey {
		t.Fatalf("key changed: %v", err)
	}

	// The token is never shown again, logged or recorded.
	list, err := svc.AppPasswords(ctx, bob, w.bobBox)
	if err != nil || len(list) != 1 || list[0].ID != ap.ID {
		t.Fatalf("list %+v %v", list, err)
	}
	b, _ := json.Marshal(list)
	mb, _ := svc.MailboxOf(ctx, bob, w.bobBox)
	b2, _ := json.Marshal(mb)
	for what, s := range map[string]string{"list": string(b), "mailbox": string(b2), "activity": activityText(t, w), "logs": logs.String()} {
		if strings.Contains(s, secret) || strings.Contains(s, pm.AppPasswords[0].MAC) {
			t.Errorf("%s contains the token or MAC", what)
		}
	}
	if act, target, detail, _ := lastActivity(t, w); act != "mail.mailbox.app-password-create" || target != "info@bobsite.example.com" || detail != "Phone" {
		t.Fatalf("activity %s %s %s", act, target, detail)
	}

	// Permissions: others' mailboxes are not found; read-only tokens and
	// the system cannot create.
	if _, err := svc.CreateAppPassword(ctx, bob, w.rootBox, "x"); KindOf(err) != NotFound {
		t.Fatalf("other's mailbox: %v", err)
	}
	if _, err := svc.AppPasswords(ctx, bob, w.rootBox); KindOf(err) != NotFound {
		t.Fatalf("list other's: %v", err)
	}
	for _, a := range []Actor{bobRead, bobLimited, System} {
		if _, err := svc.CreateAppPassword(ctx, a, w.bobBox, "x"); KindOf(err) != Forbidden {
			t.Errorf("%s: %v", a.Name, err)
		}
	}
	if _, err := svc.CreateAppPassword(ctx, bob, w.bobBox, ""); KindOf(err) != Invalid {
		t.Fatalf("empty name: %v", err)
	}

	// Revoke.
	if err := svc.RevokeAppPassword(ctx, bob, w.rootBox, ap.ID); KindOf(err) != NotFound {
		t.Fatalf("revoke via other mailbox: %v", err)
	}
	if err := svc.RevokeAppPassword(ctx, bob, w.bobBox, ap.ID); err != nil {
		t.Fatal(err)
	}
	if pm := pushedMailbox(t, w.mail.last(), "info@bobsite.example.com"); len(pm.AppPasswords) != 0 {
		t.Fatalf("still pushed %+v", pm.AppPasswords)
	}
	if err := svc.RevokeAppPassword(ctx, bob, w.bobBox, ap.ID); KindOf(err) != NotFound {
		t.Fatalf("revoke twice: %v", err)
	}
}

// TestAccountEvents: changes the mailbox made in the mail service arrive
// as events; the panel records them in order, once, and pushes them with
// the revision so the mail service can drop its own copy.
func TestAccountEvents(t *testing.T) {
	ctx := context.Background()
	w := newMailWorld(t)
	svc := w.svc
	box, _ := svc.store.Mailbox(ctx, w.bobBox)
	key, err := svc.appPasswordKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	newHash := testHash() + "" // a well-formed hash; the value does not matter here
	id, secret, _ := config.NewAppPassword()
	mac := config.AppPasswordMAC(key, box.StorageID, id, secret)
	name := "Anna B."
	off := true
	at := w.now.Add(-time.Minute)
	ev := func(n int64, kind string, ch config.AccountChange) config.Event {
		return config.Event{ID: n, At: at, Kind: kind, StorageID: box.StorageID, Address: "info@bobsite.example.com", Account: &ch}
	}
	w.mail.set(func(f *fakeMail) {
		f.events = []config.Event{
			ev(1, config.EventDisplayNameChanged, config.AccountChange{Revision: 1, DisplayName: &name}),
			ev(2, config.EventAppPasswordCreated, config.AccountChange{Revision: 2, AppPassword: &config.AppPassword{ID: id, Name: "Laptop", MAC: mac, Created: at}}),
			ev(3, config.EventAppPasswordUsed, config.AccountChange{AppPasswordID: id, Network: "192.0.2.0/24"}),
			ev(4, config.EventAppPasswordsOnly, config.AccountChange{Revision: 3, AppPasswordsOnly: &off}),
			ev(5, config.EventPasswordChanged, config.AccountChange{Revision: 4, PasswordHash: newHash}),
			// A replayed revision is ignored.
			ev(6, config.EventDisplayNameChanged, config.AccountChange{Revision: 2, DisplayName: strp("old")}),
			// Malformed: ignored.
			ev(7, config.EventDisplayNameChanged, config.AccountChange{Revision: 9, DisplayName: strp("a\nb")}),
		}
	})
	if err := svc.recordMailEvents(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.store.Mailbox(ctx, w.bobBox)
	if got.DisplayName != name || !got.AppPasswordsOnly || got.PasswordHash != newHash || got.Revision != 4 {
		t.Fatalf("mailbox %+v", got)
	}
	aps, _ := svc.AppPasswords(ctx, bob, w.bobBox)
	if len(aps) != 1 || aps[0].Name != "Laptop" || aps[0].LastUsedNetwork != "192.0.2.0/24" || aps[0].LastUsedAt == nil {
		t.Fatalf("app passwords %+v", aps)
	}
	// Pushed right away with the revision.
	pm := pushedMailbox(t, w.mail.last(), "info@bobsite.example.com")
	if pm.Revision != 4 || pm.DisplayName != name || len(pm.AppPasswords) != 1 || pm.AppPasswords[0].MAC != mac {
		t.Fatalf("pushed %+v", pm)
	}
	text := activityText(t, w)
	if strings.Contains(text, newHash) || strings.Contains(text, mac) || !strings.Contains(text, "mail.mailbox.app-password-created") {
		t.Fatalf("activity %s", text)
	}

	// An admin resets the password; a change the mailbox made before that
	// (but reported later) does not override it, though its revision is
	// recorded so the mail service drops its copy.
	w.now = w.now.Add(time.Minute)
	if err := svc.SetMailboxPassword(ctx, bob, w.bobBox, "admin-password-1"); err != nil {
		t.Fatal(err)
	}
	adminHash, _ := svc.store.Mailbox(ctx, w.bobBox)
	w.mail.set(func(f *fakeMail) {
		f.events = append(f.events,
			config.Event{ID: 8, At: w.now.Add(-time.Second), Kind: config.EventPasswordChanged, StorageID: box.StorageID,
				Account: &config.AccountChange{Revision: 5, PasswordHash: testHash()}},
			config.Event{ID: 9, At: w.now, Kind: config.EventAppPasswordRevoked, StorageID: box.StorageID,
				Account: &config.AccountChange{Revision: 6, AppPasswordID: id}},
		)
	})
	if err := svc.recordMailEvents(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.store.Mailbox(ctx, w.bobBox)
	if got.PasswordHash != adminHash.PasswordHash || got.Revision != 6 {
		t.Fatalf("admin reset overridden: %+v", got)
	}
	if aps, _ := svc.AppPasswords(ctx, bob, w.bobBox); len(aps) != 0 {
		t.Fatalf("revoked still there %+v", aps)
	}
	if !strings.Contains(activityText(t, w), "not taken: changed in the panel at the same time") {
		t.Fatal("override not recorded")
	}
	if pm := pushedMailbox(t, w.mail.last(), "info@bobsite.example.com"); pm.Revision != 6 || pm.PasswordHash != adminHash.PasswordHash {
		t.Fatalf("pushed %+v", pm)
	}
}

// TestLongStandaloneDomain: a domain of the longest possible name that
// stands alone is pushed without an organisation id (the mail service
// falls back to the domain), so it cannot fail the push for everyone.
func TestLongStandaloneDomain(t *testing.T) {
	ctx := context.Background()
	w := newMailWorld(t)
	long := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 57) + ".com"
	if len(long) != 253 || !config.ValidDomain(long) {
		t.Fatalf("test domain %d", len(long))
	}
	w.mx.point(long, testMailHost)
	if _, err := w.svc.EnableMailDomain(ctx, admin, long); err != nil {
		t.Fatal(err)
	}
	cfg := w.mail.last()
	if o := pushedDomain(t, cfg, long).Organisation; o != "" {
		t.Fatalf("standalone pushed as %q", o)
	}
	if o := pushedDomain(t, cfg, "bobsite.example.com").Organisation; o != "" {
		t.Fatalf("standalone pushed as %q", o)
	}
}

// TestAppPasswordEventOverLimit: an app password the mail service reports
// for a mailbox that already has the most it may have (created in the
// panel at the same time) is not recorded, but its revision is, and the
// push still works for everyone.
func TestAppPasswordEventOverLimit(t *testing.T) {
	ctx := context.Background()
	w := newMailWorld(t)
	svc := w.svc
	box, _ := svc.store.Mailbox(ctx, w.bobBox)
	for i := 0; i < config.MaxAppPasswords; i++ {
		if _, err := svc.CreateAppPassword(ctx, bob, w.bobBox, "Device"); err != nil {
			t.Fatal(err)
		}
	}
	key, _ := svc.appPasswordKey(ctx)
	id, secret, _ := config.NewAppPassword()
	w.mail.set(func(f *fakeMail) {
		f.events = []config.Event{{ID: 1, At: w.now, Kind: config.EventAppPasswordCreated, StorageID: box.StorageID,
			Account: &config.AccountChange{Revision: 1, AppPassword: &config.AppPassword{
				ID: id, Name: "One too many", MAC: config.AppPasswordMAC(key, box.StorageID, id, secret), Created: w.now}}}}
	})
	if err := svc.recordMailEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if aps, _ := svc.store.AppPasswords(ctx, w.bobBox); len(aps) != config.MaxAppPasswords {
		t.Fatalf("%d app passwords", len(aps))
	}
	got, _ := svc.store.Mailbox(ctx, w.bobBox)
	if got.Revision != 1 {
		t.Fatalf("revision %d", got.Revision)
	}
	if !strings.Contains(activityText(t, w), "not taken: the mailbox has the most app passwords it may have") {
		t.Fatalf("not noted: %s", activityText(t, w))
	}
	if pm := pushedMailbox(t, w.mail.last(), "info@bobsite.example.com"); pm.Revision != 1 || len(pm.AppPasswords) != config.MaxAppPasswords {
		t.Fatalf("pushed revision %d, %d app passwords", pm.Revision, len(pm.AppPasswords))
	}
}

// TestBuildMailConfigSkipsBadMailbox: data of one mailbox or domain that
// the mail service would refuse never stops the push for everyone; it is
// left out or cut down, with a log line.
func TestBuildMailConfigSkipsBadMailbox(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(old)
	w := newMailWorld(t)
	svc := w.svc
	key, _ := svc.appPasswordKey(ctx)
	box, _ := svc.store.Mailbox(ctx, w.bobBox)
	for i := 0; i < config.MaxAppPasswords+3; i++ {
		id, secret, _ := config.NewAppPassword()
		if _, err := svc.store.CreateAppPassword(ctx, store.AppPassword{ID: id, MailboxID: box.ID, Name: "Device",
			MAC: config.AppPasswordMAC(key, box.StorageID, id, secret)}, 0); err != nil {
			t.Fatal(err)
		}
	}
	// A mailbox whose stored hash is damaged.
	_, err := svc.store.CreateMailbox(ctx, store.Mailbox{StorageID: newStorageID(), Domain: "bobsite.example.com", LocalPart: "broken",
		PasswordHash: "not-a-hash", SendPerHour: 1, SendPerDay: 1}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.mailSync(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := w.mail.last()
	if pm := pushedMailbox(t, cfg, "info@bobsite.example.com"); len(pm.AppPasswords) != config.MaxAppPasswords {
		t.Fatalf("%d app passwords pushed", len(pm.AppPasswords))
	}
	pushedMailbox(t, cfg, "root@rootsite.example.com")
	for _, m := range cfg.Mailboxes {
		if m.Address == "broken@bobsite.example.com" {
			t.Fatal("broken mailbox pushed")
		}
	}
	if !strings.Contains(logs.String(), "broken@bobsite.example.com") || !strings.Contains(logs.String(), "info@bobsite.example.com") {
		t.Fatalf("not logged: %s", logs.String())
	}
	// The status still compares the same configuration.
	st, err := svc.MailStatus(ctx, admin)
	if err != nil || !st.InSync {
		t.Fatalf("status %+v %v", st, err)
	}
}

// TestAdminChangeKeepsOtherFields: an admin change of one field does not
// discard the mailbox's own change of another field reported later (a
// leaked password the mailbox replaced must not work again because an
// admin renamed the mailbox).
func TestAdminChangeKeepsOtherFields(t *testing.T) {
	ctx := context.Background()
	w := newMailWorld(t)
	svc := w.svc
	box, _ := svc.store.Mailbox(ctx, w.bobBox)
	selfAt := w.now
	w.now = w.now.Add(time.Minute)
	// The admin renames the mailbox and turns the switch on, after the
	// mailbox changed its password and its display name.
	if _, err := svc.SetMailboxAccount(ctx, bob, w.bobBox, MailboxAccount{DisplayName: strp("Team")}); err != nil {
		t.Fatal(err)
	}
	on := true
	if _, err := svc.SetMailboxAccount(ctx, bob, w.bobBox, MailboxAccount{AppPasswordsOnly: &on}); err != nil {
		t.Fatal(err)
	}
	newHash, err := hashPassword("the-mailbox-replaced-it")
	if err != nil {
		t.Fatal(err)
	}
	w.mail.set(func(f *fakeMail) {
		f.events = []config.Event{
			{ID: 1, At: selfAt, Kind: config.EventPasswordChanged, StorageID: box.StorageID,
				Account: &config.AccountChange{Revision: 1, PasswordHash: newHash}},
			{ID: 2, At: selfAt, Kind: config.EventDisplayNameChanged, StorageID: box.StorageID,
				Account: &config.AccountChange{Revision: 2, DisplayName: strp("Mine")}},
		}
	})
	if err := svc.recordMailEvents(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.store.Mailbox(ctx, w.bobBox)
	if got.PasswordHash != newHash {
		t.Fatal("the mailbox's own password change was discarded by an unrelated admin change")
	}
	if got.DisplayName != "Team" || !got.AppPasswordsOnly || got.Revision != 2 {
		t.Fatalf("mailbox %+v", got)
	}
}

// TestAdminResetCatchesUpEvents: before an admin's password reset is
// pushed, the panel records what the mailbox changed in the mail service,
// so the push carries a revision that includes it and the reset takes
// effect at once. The push also says when the admin set the password.
func TestAdminResetCatchesUpEvents(t *testing.T) {
	ctx := context.Background()
	w := newMailWorld(t)
	svc := w.svc
	box, _ := svc.store.Mailbox(ctx, w.bobBox)
	selfHash, _ := hashPassword("chosen-by-the-mailbox")
	w.mail.set(func(f *fakeMail) {
		f.events = []config.Event{{ID: 1, At: w.now.Add(-time.Minute), Kind: config.EventPasswordChanged, StorageID: box.StorageID,
			Account: &config.AccountChange{Revision: 3, PasswordHash: selfHash}}}
	})
	if err := svc.SetMailboxPassword(ctx, bob, w.bobBox, "admin-password-1"); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.store.Mailbox(ctx, w.bobBox)
	pm := pushedMailbox(t, w.mail.last(), "info@bobsite.example.com")
	if pm.Revision != 3 || pm.PasswordHash != got.PasswordHash || got.PasswordHash == selfHash {
		t.Fatalf("pushed revision %d", pm.Revision)
	}
	if pm.PasswordSetAt != w.now.Unix() || pm.DisplayNameSetAt != 0 {
		t.Fatalf("set at %d %d", pm.PasswordSetAt, pm.DisplayNameSetAt)
	}
	// The mailbox's earlier change is in the activity log as taken, before
	// the reset.
	if !strings.Contains(activityText(t, w), "mail.mailbox.password-changed") {
		t.Fatal("event not recorded")
	}
	if act, _, _, _ := lastActivity(t, w); act != "mail.mailbox.password" {
		t.Fatalf("last %s", act)
	}
}

// TestFailingAccountEventDoesNotBlock: an account event that cannot be
// recorded holds back only the later events of the same mailbox; the rest
// goes on, and the held back ones are recorded in order later.
func TestFailingAccountEventDoesNotBlock(t *testing.T) {
	ctx := context.Background()
	w := newMailWorld(t)
	svc := w.svc
	bobBox, _ := svc.store.Mailbox(ctx, w.bobBox)
	rootBox, _ := svc.store.Mailbox(ctx, w.rootBox)
	fail := true
	svc.failAccountEvent = func(e config.Event) error {
		if fail && e.StorageID == bobBox.StorageID {
			return errors.New("disk full")
		}
		return nil
	}
	ev := func(id int64, sid string, rev int64, name string) config.Event {
		return config.Event{ID: id, At: w.now, Kind: config.EventDisplayNameChanged, StorageID: sid,
			Account: &config.AccountChange{Revision: rev, DisplayName: strp(name)}}
	}
	w.mail.set(func(f *fakeMail) {
		f.events = []config.Event{
			ev(1, bobBox.StorageID, 1, "First"),
			ev(2, rootBox.StorageID, 1, "Root"),
			ev(3, bobBox.StorageID, 2, "Second"),
			{ID: 4, At: w.now, Kind: "listener.failed", Detail: "port 25"},
		}
	})
	if err := svc.recordMailEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.store.Mailbox(ctx, w.rootBox); got.DisplayName != "Root" {
		t.Fatal("a failing event of another mailbox blocked this one")
	}
	if !strings.Contains(activityText(t, w), "port 25") {
		t.Fatal("a failing account event blocked other events")
	}
	if got, _ := svc.store.Mailbox(ctx, w.bobBox); got.Revision != 0 {
		t.Fatalf("bob %+v", got)
	}
	// Still failing: nothing changes, nothing is lost.
	if err := svc.recordMailEvents(ctx); err != nil {
		t.Fatal(err)
	}
	fail = false
	if err := svc.recordMailEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.store.Mailbox(ctx, w.bobBox); got.DisplayName != "Second" || got.Revision != 2 {
		t.Fatalf("held back events not recorded in order: %+v", got)
	}
	if pm := pushedMailbox(t, w.mail.last(), "info@bobsite.example.com"); pm.Revision != 2 {
		t.Fatalf("pushed revision %d", pm.Revision)
	}
	if m, _ := svc.store.Settings(ctx); m[keyMailEventsPending] != "" {
		t.Fatalf("pending left: %s", m[keyMailEventsPending])
	}
}
