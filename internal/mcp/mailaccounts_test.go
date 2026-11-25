package mcp

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/ownpanel/internal/secrets"
)

func TestMailAccountTools(t *testing.T) {
	e := newEnvWith(t, "", func(c *panel.Config) {
		box, err := secrets.Open(filepath.Join(t.TempDir(), secrets.KeyFile))
		if err != nil {
			t.Fatal(err)
		}
		c.Secrets, c.MXResolver, c.MailHost = box, mxHere{}, "mail.example.net"
	})
	ctx := context.Background()
	cs := e.connect(t, e.write)
	var d mailDomainOutput
	ok(t, call_(t, cs, "enable_mail_domain", map[string]any{"domain": "example.com"}), &d)

	var org organisationOutput
	ok(t, call_(t, cs, "set_mail_organisation", map[string]any{"name": "Example Ltd"}), &org)
	ok(t, call_(t, cs, "set_mail_domain_organisation", map[string]any{"domain": "example.com", "organisation": org.Organisation.ID}), &d)
	if d.MailDomain.Organisation != "Example Ltd" || d.MailDomain.OrganisationID != org.Organisation.ID {
		t.Fatalf("domain %+v", d)
	}
	ok(t, call_(t, cs, "set_mail_organisation", map[string]any{"id": org.Organisation.ID, "name": "Example GmbH"}), &org)
	var orgs organisationsOutput
	ok(t, call_(t, cs, "list_mail_organisations", nil), &orgs)
	if len(orgs.Organisations) != 1 || orgs.Organisations[0].Name != "Example GmbH" || len(orgs.Organisations[0].Domains) != 1 {
		t.Fatalf("orgs %+v", orgs)
	}
	refused(t, call_(t, cs, "delete_mail_organisation", map[string]any{"id": org.Organisation.ID, "confirm_name": "x"}), "does not match")
	var del deletedOutput
	ok(t, call_(t, cs, "delete_mail_organisation", map[string]any{"id": org.Organisation.ID, "confirm_name": "Example GmbH"}), &del)

	box, err := e.svc.CreateMailbox(ctx, e.admin, "example.com", panel.MailboxInput{LocalPart: "info", Password: "mailbox-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	var mb mailboxOutput
	ok(t, call_(t, cs, "set_mailbox_display_name", map[string]any{"id": box.ID, "display_name": "Info Team"}), &mb)
	if mb.Mailbox.DisplayName != "Info Team" {
		t.Fatalf("mailbox %+v", mb)
	}
	ro := e.connect(t, e.read)
	refused(t, call_(t, ro, "set_mailbox_display_name", map[string]any{"id": box.ID, "display_name": "x"}), "can only read")
}
