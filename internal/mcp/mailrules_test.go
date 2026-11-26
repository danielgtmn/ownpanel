package mcp

import (
	"context"
	"path/filepath"
	"testing"

	mailconfig "github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/panzerschrank"
)

// rulesMail is a mail service that keeps one out-of-office reply.
type rulesMail struct {
	importMail
	rules mailconfig.MailRules
}

func (f *rulesMail) MailRules(context.Context, string) (mailconfig.MailRules, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rules, nil
}

func (f *rulesMail) SetOutOfOffice(_ context.Context, _ string, ch mailconfig.OutOfOfficeChange) (mailconfig.MailRules, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules.Managed, f.rules.OutOfOffice = true, ch.OutOfOffice
	return f.rules, nil
}

func (f *rulesMail) SetForwarding(_ context.Context, _ string, ch mailconfig.ForwardingChange) (mailconfig.MailRules, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules.Forwarding = ch.Forwarding
	return f.rules, nil
}

func TestOutOfOfficeTools(t *testing.T) {
	mail := &rulesMail{rules: mailconfig.MailRules{Managed: true}}
	e := newEnvWith(t, "", func(c *panel.Config) {
		box, err := panzerschrank.OpenKeyFile(filepath.Join(t.TempDir(), panzerschrank.KeyFile))
		if err != nil {
			t.Fatal(err)
		}
		c.Secrets, c.MXResolver, c.MailHost, c.Mail = box, mxHere{}, "mail.example.net", mail
	})
	ctx := context.Background()
	if _, err := e.svc.EnableMailDomain(ctx, e.admin, "example.com"); err != nil {
		t.Fatal(err)
	}
	box, err := e.svc.CreateMailbox(ctx, e.admin, "example.com", panel.MailboxInput{LocalPart: "info", Password: "mailbox-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	ro := e.connect(t, e.read)
	refused(t, call_(t, ro, "set_mailbox_out_of_office", map[string]any{"id": box.ID, "text": "Gone."}), "can only read")
	cs := e.connect(t, e.write)
	var out outOfOfficeOutput
	ok(t, call_(t, cs, "set_mailbox_out_of_office", map[string]any{"id": box.ID, "text": "Anna has left; write to info@.", "end": "2099-12-31"}), &out)
	if !out.OutOfOffice.Enabled || !out.OutOfOffice.On || out.OutOfOffice.SetBy == "" || out.OutOfOffice.End != "2099-12-31" {
		t.Fatalf("set %+v", out)
	}
	refused(t, call_(t, cs, "set_mailbox_out_of_office", map[string]any{"id": box.ID, "text": "x", "days": 500}), "90 days")
	ok(t, call_(t, ro, "get_mailbox_out_of_office", map[string]any{"id": box.ID}), &out)
	if !out.OutOfOffice.On {
		t.Fatalf("get %+v", out)
	}
	ok(t, call_(t, cs, "clear_mailbox_out_of_office", map[string]any{"id": box.ID}), &out)
	if out.OutOfOffice.Enabled || mail.rules.OutOfOffice.Enabled {
		t.Fatalf("clear %+v", out)
	}
}
