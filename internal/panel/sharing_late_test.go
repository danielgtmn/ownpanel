package panel

import (
	"context"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// TestLateAccountEventsOnSharedMailbox: changes a mailbox made while it was
// personal, reported after it became shared, give it no password and no
// app password, and it stays in the configuration.
func TestLateAccountEventsOnSharedMailbox(t *testing.T) {
	ctx := context.Background()
	w := newMailWorld(t)
	svc := w.svc
	box, _ := svc.store.Mailbox(ctx, w.bobBox)
	if _, err := svc.SetMailboxKind(ctx, bob, w.bobBox, "shared", ""); err != nil {
		t.Fatal(err)
	}
	key, err := svc.appPasswordKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, secret, _ := config.NewAppPassword()
	mac := config.AppPasswordMAC(key, box.StorageID, id, secret)
	at := w.now.Add(time.Minute)
	only := true
	w.mail.set(func(f *fakeMail) {
		f.events = []config.Event{
			{ID: 1, At: at, Kind: config.EventAppPasswordCreated, StorageID: box.StorageID,
				Account: &config.AccountChange{Revision: 5, AppPassword: &config.AppPassword{ID: id, Name: "Old phone", MAC: mac, Created: at}}},
			{ID: 2, At: at, Kind: config.EventPasswordChanged, StorageID: box.StorageID,
				Account: &config.AccountChange{Revision: 6, PasswordHash: testHash()}},
			{ID: 3, At: at, Kind: config.EventAppPasswordsOnly, StorageID: box.StorageID,
				Account: &config.AccountChange{Revision: 7, AppPasswordsOnly: &only}},
		}
	})
	if err := svc.recordMailEvents(ctx); err != nil {
		t.Fatal(err)
	}
	m, _ := svc.store.Mailbox(ctx, w.bobBox)
	aps, _ := svc.store.AppPasswords(ctx, w.bobBox)
	if m.PasswordHash != "" || m.AppPasswordsOnly || len(aps) != 0 {
		t.Fatalf("shared mailbox got hash %q, only %v, %d app passwords", m.PasswordHash, m.AppPasswordsOnly, len(aps))
	}
	cfg, err := svc.buildMailConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p := pushedMailbox(t, cfg, "info@bobsite.example.com"); p.Kind != "shared" || p.PasswordHash != "" {
		t.Errorf("pushed %+v", p)
	}
}
