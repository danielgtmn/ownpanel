package managesieve

import (
	"context"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
)

// With the Manage right (G3), the authorization identity of PLAIN names
// another mailbox, such as a shared one, whose scripts the session then
// works on; the right is checked again on every command.
func TestManagedMailbox(t *testing.T) {
	e := start(t)
	idTeam, idBob := mailtest.ID('d'), mailtest.ID('b')
	load := func(rights ...string) {
		t.Helper()
		cfg := *e.cfg
		cfg.Mailboxes = append(append([]config.Mailbox{}, cfg.Mailboxes...),
			config.Mailbox{StorageID: idBob, Address: "bob@example.org", PasswordHash: mailtest.CheapHash("bob password")},
			config.Mailbox{StorageID: idTeam, Address: "team@example.org", Kind: config.KindShared, Grants: []config.Grant{
				{StorageID: idAlice, Rights: rights},
				{StorageID: idBob, Rights: []string{config.RightRead, config.RightWrite}},
			}},
		)
		e.dir.Store(directory.New(&cfg))
	}
	load(config.RightRead, config.RightManage)

	c := e.dial()
	c.starttls()
	if _, f := c.cmd(`AUTHENTICATE "PLAIN" "` + plain("Team@example.org", alice, password) + `"`); !strings.HasPrefix(f, "OK") {
		t.Fatalf("alice for team: %q", f)
	}
	c.ok(`PUTSCRIPT "office" ` + lit(`keep;`))
	c.ok(`SETACTIVE "office"`)
	ctx := context.Background()
	if name, _, ok, err := e.store.ActiveSieveScript(ctx, idTeam); err != nil || !ok || name != "office" {
		t.Fatalf("team's active script %q %v %v", name, ok, err)
	}
	if list, _ := e.store.SieveScripts(ctx, idAlice); len(list) != 0 {
		t.Fatalf("alice's own scripts changed: %+v", list)
	}

	// Without Manage, or for a mailbox without a grant: refused after the
	// password, like a wrong one.
	for _, tc := range []struct{ authz, user, pass string }{
		{"team@example.org", "bob@example.org", "bob password"},
		{"bob@example.org", alice, password},
		{"nobody@example.org", alice, password},
	} {
		b := e.dial()
		b.starttls()
		if _, f := b.cmd(`AUTHENTICATE "PLAIN" "` + plain(tc.authz, tc.user, tc.pass) + `"`); !strings.HasPrefix(f, "NO") {
			t.Errorf("%s for %s: %q", tc.user, tc.authz, f)
		}
	}

	// The right is taken away: the next command ends the session.
	load(config.RightRead)
	if _, f := c.cmd(`LISTSCRIPTS`); !strings.HasPrefix(f, "BYE") {
		t.Fatalf("after Manage was removed: %q", f)
	}
}
