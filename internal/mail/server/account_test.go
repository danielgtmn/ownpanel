package server

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
)

func strp(s string) *string { return &s }

// newAppPassword makes an app password of alice under key.
func newAppPassword(key, name string) (config.AppPassword, string) {
	id, secret, token := config.NewAppPassword()
	return config.AppPassword{ID: id, Name: name, MAC: config.AppPasswordMAC(key, idAlice, id, secret), Created: time.Now().UTC().Truncate(time.Second)}, token
}

func accountEvents(t *testing.T, in *instance) []config.Event {
	t.Helper()
	evs, err := in.client.Events(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []config.Event
	for _, e := range evs {
		if e.Account != nil {
			out = append(out, e)
		}
	}
	return out
}

// TestAccountChangeRace: a mailbox changes its own account here while the
// panel pushes a configuration it built before it saw the change. The
// change must survive that push and a restart, and give way once a push
// includes it (or the panel decided otherwise for the same revision).
func TestAccountChangeRace(t *testing.T) {
	ctx := context.Background()
	data, sock := t.TempDir(), socketDir(t)
	in := run(t, data, sock)
	cfg := testConfig(t)
	cfg.AppPasswordKey = config.NewAppPasswordKey()
	cfg.Mailboxes[0].PasswordHash = mailtest.CheapHash("old-password")
	cfg.Mailboxes[0].Revision = 4
	if err := in.client.Load(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	// The mailbox sets a new password and creates an app password.
	newHash := mailtest.CheapHash("new-password")
	ch, err := in.srv.ApplyAccountChange(ctx, idAlice, config.AccountChange{PasswordHash: newHash})
	if err != nil || ch.Revision != 5 {
		t.Fatalf("password: %+v %v", ch, err)
	}
	ap, token := newAppPassword(cfg.AppPasswordKey, "Laptop")
	if ch, err = in.srv.ApplyAccountChange(ctx, idAlice, config.AccountChange{AppPassword: &ap}); err != nil || ch.Revision != 6 {
		t.Fatalf("app password: %+v %v", ch, err)
	}
	m, _ := in.srv.Directory().MailboxByStorageID(idAlice)
	if m.PasswordHash != newHash || len(m.AppPasswords) != 1 {
		t.Fatalf("not applied: %+v", m)
	}
	// Reported with the hash and MAC, never a password, in order.
	evs := accountEvents(t, in)
	if len(evs) != 2 || evs[0].Kind != config.EventPasswordChanged || evs[0].Account.Revision != 5 || evs[0].Account.PasswordHash != newHash ||
		evs[1].Kind != config.EventAppPasswordCreated || evs[1].Account.Revision != 6 || evs[1].Account.AppPassword.MAC != ap.MAC ||
		evs[1].Detail != "Laptop" || strings.Contains(evs[1].Detail+evs[0].Detail, token) {
		t.Fatalf("events %+v", evs)
	}
	// Status reports the hash of what the panel pushed, so the panel does
	// not push again and again because of the overlay.
	if st, _ := in.client.Status(ctx); st.ConfigHash != cfg.Hash() {
		t.Fatal("status hash includes local changes")
	}

	// The race: a push with the old values (revision 4) arrives after the
	// change. The change stays.
	cfg.Limits.MaxRecipients = 7 // something else did change
	if err := in.client.Load(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	m, _ = in.srv.Directory().MailboxByStorageID(idAlice)
	if m.PasswordHash != newHash || len(m.AppPasswords) != 1 || in.srv.Directory().Limits().MaxRecipients != 7 {
		t.Fatalf("old push undid the change: %+v", m)
	}

	// The app password works for IMAP; a restart keeps both changes.
	imapLogin := func(user, pw string) (*imapclient.Client, error) {
		st, _ := in.client.Status(ctx)
		ic, err := imapclient.DialTLS(listener(st, "imaps").Address, &imapclient.Options{TLSConfig: &tls.Config{InsecureSkipVerify: true}})
		if err != nil {
			t.Fatal(err)
		}
		if err := ic.Login(user, pw).Wait(); err != nil {
			ic.Close()
			return nil, err
		}
		return ic, nil
	}
	in.stop(t)
	in = run(t, data, sock)
	m, _ = in.srv.Directory().MailboxByStorageID(idAlice)
	if m.PasswordHash != newHash || len(m.AppPasswords) != 1 {
		t.Fatalf("restart lost the change: %+v", m)
	}
	ic, err := imapLogin("alice@example.org", token)
	if err != nil {
		t.Fatalf("app password: %v", err)
	}
	if err := ic.Noop().Wait(); err != nil {
		t.Fatal(err)
	}

	// Revoking it ends the open session at once.
	if _, err := in.srv.ApplyAccountChange(ctx, idAlice, config.AccountChange{AppPasswordID: ap.ID}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := ic.Noop().Wait(); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session with a revoked app password still works")
		}
		time.Sleep(20 * time.Millisecond)
	}
	ic.Close()
	if _, err := imapLogin("alice@example.org", token); err == nil {
		t.Fatal("revoked app password signs in")
	}

	// The panel recorded everything up to revision 5 and, at the same
	// time, an admin reset the password: from revision 5 on the panel's
	// value wins; the revocation (7) still holds.
	adminHash := mailtest.CheapHash("admin-password")
	cfg.Mailboxes[0].PasswordHash, cfg.Mailboxes[0].Revision = adminHash, 5
	cfg.Mailboxes[0].AppPasswords = []config.AppPassword{ap} // the panel has not seen the revocation
	if err := in.client.Load(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	m, _ = in.srv.Directory().MailboxByStorageID(idAlice)
	if m.PasswordHash != adminHash || len(m.AppPasswords) != 0 {
		t.Fatalf("after push of revision 5: %+v", m)
	}
	// Once a push includes all changes the overlay is gone for good.
	cfg.Mailboxes[0].Revision, cfg.Mailboxes[0].AppPasswords = 7, nil
	if err := in.client.Load(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if n := len(in.srv.overlays); n != 0 {
		t.Fatalf("%d overlays left", n)
	}
	if ov, _ := in.srv.store.AccountOverlays(ctx); len(ov) != 0 {
		t.Fatal("overlay left in mail.db")
	}
	in.stop(t)
}

func TestAccountChangeChecks(t *testing.T) {
	ctx := context.Background()
	in := run(t, t.TempDir(), socketDir(t))
	defer in.stop(t)
	cfg := testConfig(t)
	if err := in.client.Load(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	ap, _ := newAppPassword(config.NewAppPasswordKey(), "Phone")
	only := true
	for name, c := range map[string]struct {
		sid string
		ch  config.AccountChange
		err error
	}{
		"unknown mailbox":  {idBob, config.AccountChange{DisplayName: strp("x")}, ErrUnknownMailbox},
		"nothing":          {idAlice, config.AccountChange{}, ErrInvalidChange},
		"two at once":      {idAlice, config.AccountChange{DisplayName: strp("x"), AppPasswordsOnly: &only}, ErrInvalidChange},
		"bad name":         {idAlice, config.AccountChange{DisplayName: strp("a\r\nb")}, ErrInvalidChange},
		"bad hash":         {idAlice, config.AccountChange{PasswordHash: "plain"}, ErrInvalidChange},
		"no key":           {idAlice, config.AccountChange{AppPassword: &ap}, ErrNoAppPasswordKey},
		"unknown revoke":   {idAlice, config.AccountChange{AppPasswordID: ap.ID}, ErrInvalidChange},
		"network smuggled": {idAlice, config.AccountChange{DisplayName: strp("x"), Network: "1.2.3.0/24"}, ErrInvalidChange},
	} {
		if _, err := in.srv.ApplyAccountChange(ctx, c.sid, c.ch); !errors.Is(err, c.err) {
			t.Errorf("%s: %v, want %v", name, err, c.err)
		}
	}
	if evs := accountEvents(t, in); len(evs) != 0 {
		t.Fatalf("refused changes reported: %+v", evs)
	}
	// A display name and the switch are applied like the rest.
	if _, err := in.srv.ApplyAccountChange(ctx, idAlice, config.AccountChange{DisplayName: strp("Alice A.")}); err != nil {
		t.Fatal(err)
	}
	if _, err := in.srv.ApplyAccountChange(ctx, idAlice, config.AccountChange{AppPasswordsOnly: &only}); err != nil {
		t.Fatal(err)
	}
	m, _ := in.srv.Directory().MailboxByStorageID(idAlice)
	if m.DisplayName != "Alice A." || !m.AppPasswordsOnly {
		t.Fatalf("%+v", m)
	}
	if o, ok := in.srv.Directory().OrganisationOf(idAlice); !ok || o != config.StandaloneOrganisation("example.org") {
		t.Fatalf("organisation %q", o)
	}
}
