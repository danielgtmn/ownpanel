package panel

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

func TestSharedMailboxAndGrants(t *testing.T) {
	ctx := context.Background()
	w := newMailWorld(t)
	svc := w.svc

	// A shared mailbox has no password; only it keeps sent copies.
	if _, err := svc.CreateMailbox(ctx, bob, "bobsite.example.com", MailboxInput{LocalPart: "team", Kind: "shared", Password: "mailbox-password-1"}); KindOf(err) != Invalid {
		t.Fatalf("shared with password: %v", err)
	}
	if _, err := svc.CreateMailbox(ctx, bob, "bobsite.example.com", MailboxInput{LocalPart: "x", Password: "mailbox-password-1", KeepSentCopy: true}); KindOf(err) != Invalid {
		t.Fatalf("personal keeping copies: %v", err)
	}
	if _, err := svc.CreateMailbox(ctx, bob, "bobsite.example.com", MailboxInput{LocalPart: "x", Kind: "resource"}); KindOf(err) != Invalid {
		t.Fatalf("unknown kind: %v", err)
	}
	team, err := svc.CreateMailbox(ctx, bob, "bobsite.example.com", MailboxInput{LocalPart: "team", Kind: "Shared", KeepSentCopy: true, DisplayName: "Team"})
	if err != nil || team.Kind != config.KindShared || !team.KeepSentCopy {
		t.Fatalf("create shared: %+v %v", team, err)
	}
	pushed := pushedMailbox(t, w.mail.last(), "team@bobsite.example.com")
	if pushed.Kind != config.KindShared || pushed.PasswordHash != "" || !pushed.KeepSentCopy {
		t.Fatalf("pushed %+v", pushed)
	}
	if _, _, detail, _ := lastActivity(t, w); !strings.HasPrefix(detail, "shared; ") {
		t.Errorf("activity %q", detail)
	}

	// Access for a personal mailbox of the organisation.
	v, err := svc.SetMailboxGrant(ctx, bob, team.ID, " Info@BobSite.example.com ", []string{"write", "send-as"})
	if err != nil || len(v.SharedWith) != 1 || !slices.Equal(v.SharedWith[0].Rights, []string{"read", "write", "send-as"}) || !v.SharedWith[0].Active {
		t.Fatalf("grant %+v %v", v.SharedWith, err)
	}
	if act, target, detail, _ := lastActivity(t, w); act != "mail.mailbox.grant" || target != "team@bobsite.example.com" ||
		detail != "info@bobsite.example.com: read, write, send-as" {
		t.Errorf("activity %s %s %q", act, target, detail)
	}
	pushed = pushedMailbox(t, w.mail.last(), "team@bobsite.example.com")
	info := pushedMailbox(t, w.mail.last(), "info@bobsite.example.com")
	if len(pushed.Grants) != 1 || pushed.Grants[0].StorageID != info.StorageID {
		t.Fatalf("pushed grants %+v", pushed.Grants)
	}
	infoView, _ := svc.MailboxOf(ctx, bob, w.bobBox)
	if len(infoView.AccessTo) != 1 || infoView.AccessTo[0].Address != "team@bobsite.example.com" || infoView.AccessTo[0].Kind != "shared" {
		t.Errorf("access to %+v", infoView.AccessTo)
	}
	// Changing the rights.
	if _, err := svc.SetMailboxGrant(ctx, bob, team.ID, "info@bobsite.example.com", []string{"read"}); err != nil {
		t.Fatal(err)
	}
	if act, _, _, _ := lastActivity(t, w); act != "mail.mailbox.grant-change" {
		t.Errorf("activity %s", act)
	}

	// Never outside the organisation: not for the member, not for admins.
	for _, a := range []Actor{bob, admin} {
		if _, err := svc.SetMailboxGrant(ctx, a, team.ID, "root@rootsite.example.com", []string{"read"}); KindOf(err) != Invalid ||
			!strings.Contains(err.Error(), "no personal mailbox") {
			t.Errorf("%s across organisations: %v", a.Name, err)
		}
	}
	// A missing mailbox answers the same.
	if _, err := svc.SetMailboxGrant(ctx, bob, team.ID, "ghost@bobsite.example.com", []string{"read"}); KindOf(err) != Invalid ||
		!strings.Contains(err.Error(), "no personal mailbox") {
		t.Errorf("missing: %v", err)
	}
	for name, c := range map[string]struct {
		id    string
		addr  string
		right []string
	}{
		"itself":           {team.ID, "team@bobsite.example.com", []string{"read"}},
		"no rights":        {team.ID, "info@bobsite.example.com", nil},
		"unknown right":    {team.ID, "info@bobsite.example.com", []string{"admin"}},
		"personal mailbox": {w.bobBox, "info@bobsite.example.com", []string{"read"}},
		"bad address":      {team.ID, "not an address", []string{"read"}},
	} {
		if _, err := svc.SetMailboxGrant(ctx, bob, c.id, c.addr, c.right); KindOf(err) != Invalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A shared mailbox holds no access.
	team2, err := svc.CreateMailbox(ctx, bob, "bobsite.example.com", MailboxInput{LocalPart: "team2", Kind: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetMailboxGrant(ctx, bob, team2.ID, "team@bobsite.example.com", []string{"read"}); KindOf(err) != Invalid {
		t.Errorf("grant to a shared mailbox: %v", err)
	}
	// Who may: the domain's owner and admins; read-only and site tokens
	// may not; another member's mailbox does not exist for others.
	for _, c := range []struct {
		a    Actor
		want Kind
	}{{bobRead, Forbidden}, {bobLimited, Forbidden}, {nobody, Unauthenticated}} {
		if _, err := svc.SetMailboxGrant(ctx, c.a, team.ID, "info@bobsite.example.com", []string{"read"}); KindOf(err) != c.want {
			t.Errorf("%s: %v", c.a.Name, err)
		}
	}
	if _, err := svc.SetMailboxGrant(ctx, bob, w.rootBox, "info@bobsite.example.com", []string{"read"}); KindOf(err) != NotFound {
		t.Errorf("member on admin's mailbox: %v", err)
	}
	if _, err := svc.SetMailboxGrant(ctx, admin, team.ID, "info@bobsite.example.com", []string{"read", "manage"}); err != nil {
		t.Errorf("admin: %v", err)
	}

	// Grouping the domains lets admins share across them; taking a domain
	// out again keeps the grant but it no longer counts (nor is pushed).
	org, err := svc.CreateOrganisation(ctx, admin, "Both")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"bobsite.example.com", "rootsite.example.com"} {
		if _, err := svc.SetMailDomainOrganisation(ctx, admin, d, org.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.SetMailboxGrant(ctx, admin, team.ID, "root@rootsite.example.com", []string{"read"}); err != nil {
		t.Fatalf("same organisation: %v", err)
	}
	if _, err := svc.SetMailDomainOrganisation(ctx, admin, "rootsite.example.com", ""); err != nil {
		t.Fatal(err)
	}
	v, _ = svc.MailboxOf(ctx, bob, team.ID)
	active := map[string]bool{}
	for _, g := range v.SharedWith {
		active[g.Address] = g.Active
	}
	if !active["info@bobsite.example.com"] || active["root@rootsite.example.com"] {
		t.Errorf("active %v", active)
	}
	if got := pushedMailbox(t, w.mail.last(), "team@bobsite.example.com").Grants; len(got) != 1 {
		t.Errorf("pushed after regrouping %+v", got)
	}

	// Taking access away.
	rootView, _ := svc.MailboxOf(ctx, admin, w.rootBox)
	if _, err := svc.RemoveMailboxGrant(ctx, bob, team.ID, rootView.ID); err != nil {
		t.Fatal(err)
	}
	if act, _, detail, _ := lastActivity(t, w); act != "mail.mailbox.grant-remove" || detail != "root@rootsite.example.com" {
		t.Errorf("activity %s %q", act, detail)
	}
	if _, err := svc.RemoveMailboxGrant(ctx, bob, team.ID, rootView.ID); KindOf(err) != NotFound {
		t.Errorf("twice: %v", err)
	}

	// Nobody signs in to a shared mailbox.
	if err := svc.SetMailboxPassword(ctx, bob, team.ID, "mailbox-password-1"); KindOf(err) != Invalid {
		t.Errorf("password: %v", err)
	}
	if _, err := svc.CreateAppPassword(ctx, bob, team.ID, "Phone"); KindOf(err) != Invalid {
		t.Errorf("app password: %v", err)
	}
	on := true
	if _, err := svc.SetMailboxAccount(ctx, bob, team.ID, MailboxAccount{AppPasswordsOnly: &on}); KindOf(err) != Invalid {
		t.Errorf("only app passwords: %v", err)
	}
	if _, err := svc.SetMailboxKeepSentCopy(ctx, bob, w.bobBox, true); KindOf(err) != Invalid {
		t.Errorf("keep copies on a personal mailbox: %v", err)
	}
	if v, err := svc.SetMailboxKeepSentCopy(ctx, bob, team.ID, false); err != nil || v.KeepSentCopy {
		t.Errorf("keep copies off: %+v %v", v, err)
	}
}

func TestMailboxConversion(t *testing.T) {
	ctx := context.Background()
	w := newMailWorld(t)
	svc := w.svc
	team, err := svc.CreateMailbox(ctx, bob, "bobsite.example.com", MailboxInput{LocalPart: "team", Kind: "shared", KeepSentCopy: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetMailboxGrant(ctx, bob, team.ID, "info@bobsite.example.com", []string{"read"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateAppPassword(ctx, bob, w.bobBox, "Laptop"); err != nil {
		t.Fatal(err)
	}

	// Personal → shared: password, app passwords and the access it had go.
	if _, err := svc.SetMailboxKind(ctx, bob, w.bobBox, "shared", "a password"); KindOf(err) != Invalid {
		t.Errorf("shared with a password: %v", err)
	}
	v, err := svc.SetMailboxKind(ctx, bob, w.bobBox, "shared", "")
	if err != nil || v.Kind != "shared" || len(v.AccessTo) != 0 {
		t.Fatalf("to shared %+v %v", v, err)
	}
	m, _ := svc.store.Mailbox(ctx, w.bobBox)
	aps, _ := svc.store.AppPasswords(ctx, w.bobBox)
	if m.PasswordHash != "" || len(aps) != 0 {
		t.Errorf("left: hash %q, %d app passwords", m.PasswordHash, len(aps))
	}
	p := pushedMailbox(t, w.mail.last(), "info@bobsite.example.com")
	if p.Kind != "shared" || p.PasswordHash != "" || len(p.AppPasswords) != 0 {
		t.Errorf("pushed %+v", p)
	}
	if act, _, detail, _ := lastActivity(t, w); act != "mail.mailbox.kind" || !strings.Contains(detail, "signed out") {
		t.Errorf("activity %s %q", act, detail)
	}

	// Shared → personal needs a password; copies of sent mail stop.
	if _, err := svc.SetMailboxKind(ctx, bob, team.ID, "personal", ""); KindOf(err) != Invalid {
		t.Error("personal without a password")
	}
	v, err = svc.SetMailboxKind(ctx, bob, team.ID, "personal", "mailbox-password-9")
	if err != nil || v.Kind != "personal" || v.KeepSentCopy {
		t.Fatalf("to personal %+v %v", v, err)
	}
	if p := pushedMailbox(t, w.mail.last(), "team@bobsite.example.com"); p.Kind != "" || p.PasswordHash == "" || p.KeepSentCopy {
		t.Errorf("pushed %+v", p)
	}
	// Read-only tokens change nothing.
	if _, err := svc.SetMailboxKind(ctx, bobRead, team.ID, "shared", ""); KindOf(err) != Forbidden {
		t.Errorf("read token: %v", err)
	}
}

func TestSelfDelegationEvent(t *testing.T) {
	ctx := context.Background()
	w := newMailWorld(t)
	svc := w.svc
	anna, err := svc.CreateMailbox(ctx, bob, "bobsite.example.com", MailboxInput{LocalPart: "anna", Password: "mailbox-password-2"})
	if err != nil {
		t.Fatal(err)
	}
	info, _ := svc.store.Mailbox(ctx, w.bobBox)
	annaBox, _ := svc.store.Mailbox(ctx, anna.ID)
	grants := []config.Grant{
		{StorageID: annaBox.StorageID, Rights: []string{"read", "send-on-behalf"}},
		{StorageID: info.StorageID, Rights: []string{"read"}}, // itself: dropped
	}
	w.mail.set(func(f *fakeMail) {
		f.events = []config.Event{{ID: 1, At: w.now, Kind: config.EventGrantsChanged, StorageID: info.StorageID,
			Address: "info@bobsite.example.com", Detail: "shared with anna@bobsite.example.com (read, send-on-behalf)",
			Account: &config.AccountChange{Revision: 1, Grants: &grants}}}
	})
	if err := svc.recordMailEvents(ctx); err != nil {
		t.Fatal(err)
	}
	v, _ := svc.MailboxOf(ctx, bob, w.bobBox)
	if len(v.SharedWith) != 1 || v.SharedWith[0].Address != "anna@bobsite.example.com" ||
		!slices.Equal(v.SharedWith[0].Rights, []string{"read", "send-on-behalf"}) {
		t.Fatalf("shared with %+v", v.SharedWith)
	}
	if act, target, detail, actor := lastActivity(t, w); act != "mail.mailbox.grants-changed" || target != "info@bobsite.example.com" ||
		!strings.Contains(detail, "anna@") || actor != "info@bobsite.example.com" {
		t.Errorf("activity %s %s %q %s", act, target, detail, actor)
	}
	p := pushedMailbox(t, w.mail.last(), "info@bobsite.example.com")
	if len(p.Grants) != 1 || p.Revision != 1 {
		t.Errorf("pushed %+v", p)
	}
	// The domain owner may take it away (staff who left), not give it.
	if _, err := svc.SetMailboxGrant(ctx, bob, w.bobBox, "anna@bobsite.example.com", []string{"read"}); KindOf(err) != Invalid {
		t.Errorf("owner gives access to a personal mailbox: %v", err)
	}
	if _, err := svc.RemoveMailboxGrant(ctx, bob, w.bobBox, anna.ID); err != nil {
		t.Errorf("remove: %v", err)
	}
	if p := pushedMailbox(t, w.mail.last(), "info@bobsite.example.com"); len(p.Grants) != 0 {
		t.Errorf("pushed after removal %+v", p.Grants)
	}
}

func TestDistributionGroups(t *testing.T) {
	ctx := context.Background()
	w := newMailWorld(t)
	svc := w.svc
	g, err := svc.CreateMailAlias(ctx, bob, "bobsite.example.com", "staff", []string{"info@bobsite.example.com"},
		AliasGroup{SenderPolicy: " Members ", HideFromDirectory: true})
	if err != nil || g.SenderPolicy != "members" || !g.HideFromDirectory {
		t.Fatalf("create %+v %v", g, err)
	}
	if _, _, detail, _ := lastActivity(t, w); !strings.Contains(detail, "only its members") || !strings.Contains(detail, "hidden") {
		t.Errorf("activity %q", detail)
	}
	var pushed config.Alias
	for _, a := range w.mail.last().Aliases {
		if a.Address == "staff@bobsite.example.com" {
			pushed = a
		}
	}
	if pushed.SenderPolicy != "members" || !pushed.HideFromDirectory {
		t.Errorf("pushed %+v", pushed)
	}
	// Changing targets alone keeps the group settings.
	g, err = svc.UpdateMailAlias(ctx, bob, g.ID, "staff", []string{"info@bobsite.example.com", "x@example.org"})
	if err != nil || g.SenderPolicy != "members" || !g.HideFromDirectory {
		t.Fatalf("update %+v %v", g, err)
	}
	g, err = svc.UpdateMailAlias(ctx, bob, g.ID, "staff", g.Targets, AliasGroup{SenderPolicy: "organisation"})
	if err != nil || g.SenderPolicy != "organisation" || g.HideFromDirectory {
		t.Fatalf("update policy %+v %v", g, err)
	}
	for _, c := range []struct{ local, policy string }{{"*", "members"}, {"x", "friends"}} {
		if _, err := svc.CreateMailAlias(ctx, bob, "bobsite.example.com", c.local, []string{"info@bobsite.example.com"},
			AliasGroup{SenderPolicy: c.policy}); KindOf(err) != Invalid {
			t.Errorf("%s/%s: %v", c.local, c.policy, err)
		}
	}
	// Default: anyone, pushed without a policy.
	plain, err := svc.MailAliasOf(ctx, bob, w.bobAlias)
	if err != nil || plain.SenderPolicy != "anyone" {
		t.Fatalf("plain %+v %v", plain, err)
	}
	for _, a := range w.mail.last().Aliases {
		if a.Address == "sales@bobsite.example.com" && a.SenderPolicy != "" {
			t.Errorf("pushed plain %+v", a)
		}
	}
}
