package mcp

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/panzerschrank"
)

func TestSharingTools(t *testing.T) {
	e := newEnvWith(t, "", func(c *panel.Config) {
		box, err := panzerschrank.OpenKeyFile(filepath.Join(t.TempDir(), panzerschrank.KeyFile))
		if err != nil {
			t.Fatal(err)
		}
		c.Secrets, c.MXResolver, c.MailHost = box, mxHere{}, "mail.example.net"
	})
	ctx := context.Background()
	cs := e.connect(t, e.write)
	var d mailDomainOutput
	ok(t, call_(t, cs, "enable_mail_domain", map[string]any{"domain": "example.com"}), &d)
	team, err := e.svc.CreateMailbox(ctx, e.admin, "example.com", panel.MailboxInput{LocalPart: "team", Password: "mailbox-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateMailbox(ctx, e.admin, "example.com", panel.MailboxInput{LocalPart: "anna", Password: "mailbox-password-2"}); err != nil {
		t.Fatal(err)
	}
	var mb mailboxOutput
	refused(t, call_(t, cs, "set_mailbox_kind", map[string]any{"id": team.ID, "kind": "personal"}), "web interface")
	ok(t, call_(t, cs, "set_mailbox_kind", map[string]any{"id": team.ID, "kind": "shared", "keep_sent_copy": true}), &mb)
	if mb.Mailbox.Kind != "shared" || !mb.Mailbox.KeepSentCopy {
		t.Fatalf("kind %+v", mb)
	}
	ok(t, call_(t, cs, "set_mailbox_access", map[string]any{"id": team.ID, "address": "anna@example.com", "rights": []string{"read", "send-as"}}), &mb)
	if len(mb.Mailbox.SharedWith) != 1 || mb.Mailbox.SharedWith[0].Address != "anna@example.com" {
		t.Fatalf("access %+v", mb)
	}
	refused(t, call_(t, cs, "set_mailbox_access", map[string]any{"id": team.ID, "address": "x@elsewhere.example", "rights": []string{"read"}}), "no personal mailbox")
	ok(t, call_(t, cs, "remove_mailbox_access", map[string]any{"id": team.ID, "grantee_id": mb.Mailbox.SharedWith[0].MailboxID}), &mb)
	if len(mb.Mailbox.SharedWith) != 0 {
		t.Fatalf("after remove %+v", mb)
	}
	var al aliasOutput
	ok(t, call_(t, cs, "set_mail_alias", map[string]any{"domain": "example.com", "local_part": "staff", "targets": []string{"team@example.com"},
		"sender_policy": "members"}), &al)
	if al.Alias.SenderPolicy != "members" {
		t.Fatalf("alias %+v", al)
	}
	ok(t, call_(t, cs, "set_mail_alias", map[string]any{"id": al.Alias.ID, "local_part": "staff", "targets": []string{"team@example.com"},
		"hide_from_directory": true}), &al)
	if al.Alias.SenderPolicy != "members" || !al.Alias.HideFromDirectory {
		t.Fatalf("alias kept %+v", al)
	}
	ro := e.connect(t, e.read)
	refused(t, call_(t, ro, "set_mailbox_access", map[string]any{"id": team.ID, "address": "anna@example.com", "rights": []string{"read"}}), "can only read")
}
