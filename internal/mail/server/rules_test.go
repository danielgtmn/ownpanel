package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/rules"
	"github.com/danielgtmn/ownpanel/internal/mail/sieve"
)

// The panel sets out-of-office and forwarding through the admin socket;
// the status tells whether a reply is on, and delivery runs the filters.
func TestRulesOverAdminSocket(t *testing.T) {
	ctx := context.Background()
	in := run(t, t.TempDir(), socketDir(t))
	defer in.stop(t)
	if err := in.client.Load(ctx, testConfig(t)); err != nil {
		t.Fatal(err)
	}
	c := in.client

	r, err := c.MailRules(ctx, idAlice)
	if err != nil || !r.Managed || r.OutOfOffice.Enabled {
		t.Fatalf("empty: %+v %v", r, err)
	}
	var ce *config.Error
	if _, err := c.MailRules(ctx, "ffffffffffffffffffffffffffffffff"); !errors.As(err, &ce) || ce.Status != 404 {
		t.Fatalf("unknown mailbox: %v", err)
	}
	r, err = c.SetOutOfOffice(ctx, idAlice, config.OutOfOfficeChange{OutOfOffice: config.OutOfOffice{
		Enabled: true, Subject: "Left", Text: "Anna has left.", SetBy: "root"}})
	if err != nil || r.OutOfOffice.SetBy != "root" || r.OutOfOffice.SetAt.IsZero() || r.OutOfOffice.Days != 7 {
		t.Fatalf("set: %+v %v", r, err)
	}
	if _, err := c.SetOutOfOffice(ctx, idAlice, config.OutOfOfficeChange{OutOfOffice: config.OutOfOffice{Enabled: true}}); !errors.As(err, &ce) || ce.Status != 400 {
		t.Fatalf("invalid: %v", err)
	}
	st, err := c.Status(ctx)
	if err != nil || len(st.Mailboxes) != 1 || !st.Mailboxes[0].OutOfOffice {
		t.Fatalf("status %+v %v", st.Mailboxes, err)
	}
	r, err = c.SetForwarding(ctx, idAlice, config.ForwardingChange{Forwarding: config.Forwarding{Enabled: true, To: "Next@Example.net", SetBy: "root"}})
	if err != nil || r.Forwarding.To != "next@example.net" || !r.OutOfOffice.Enabled {
		t.Fatalf("forwarding: %+v %v", r, err)
	}

	// A script written in a mail program: 409 until replaced.
	store := in.srv.Store()
	if err := store.PutSieveScript(ctx, idAlice, "mine", "keep;", sieve.MaxScripts, true); err != nil {
		t.Fatal(err)
	}
	if st, _ := c.Status(ctx); st.Mailboxes[0].OutOfOffice {
		t.Fatal("reply reported on while another script runs")
	}
	if _, err := c.SetOutOfOffice(ctx, idAlice, config.OutOfOfficeChange{}); !errors.As(err, &ce) || ce.Status != 409 {
		t.Fatalf("hand written: %v", err)
	}
	r, err = c.MailRules(ctx, idAlice)
	if err != nil || r.Managed || r.Script != "keep;" || r.ActiveScript != "mine" || r.Forwarding.Enabled {
		t.Fatalf("read-only view: %+v %v", r, err)
	}
	r, err = c.SetOutOfOffice(ctx, idAlice, config.OutOfOfficeChange{Replace: true})
	if err != nil || !r.Managed || r.OutOfOffice.Enabled || !r.Forwarding.Enabled {
		t.Fatalf("replaced: %+v %v", r, err)
	}
	name, src, _, _ := store.ActiveSieveScript(ctx, idAlice)
	if name != rules.ScriptName || !strings.Contains(src, `redirect "next@example.net"`) {
		t.Fatalf("active %s:\n%s", name, src)
	}
	// The hand-written script is still there, inactive.
	if _, active, err := store.SieveScript(ctx, idAlice, "mine"); err != nil || active {
		t.Fatalf("mine: %v %v", active, err)
	}
	// Clearing everything removes the generated script.
	if _, err := c.SetForwarding(ctx, idAlice, config.ForwardingChange{}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, _ := store.ActiveSieveScript(ctx, idAlice); ok {
		t.Fatal("still active")
	}
	if _, _, err := store.SieveScript(ctx, idAlice, rules.ScriptName); err == nil {
		t.Fatal("generated script kept")
	}
}
