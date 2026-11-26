package panel

import (
	"context"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// TestPanelGrantRemovalWins: a self-delegation the mailbox made before the
// panel took the access away, reported late, does not bring it back; one
// made afterwards does.
func TestPanelGrantRemovalWins(t *testing.T) {
	ctx := context.Background()
	w := newMailWorld(t)
	svc := w.svc
	anna, err := svc.CreateMailbox(ctx, bob, "bobsite.example.com", MailboxInput{LocalPart: "anna", Password: "mailbox-password-2"})
	if err != nil {
		t.Fatal(err)
	}
	info, _ := svc.store.Mailbox(ctx, w.bobBox)
	annaBox, _ := svc.store.Mailbox(ctx, mustStoreID(anna.ID))
	grants := []config.Grant{{StorageID: annaBox.StorageID, Rights: []string{"read"}}}
	event := func(id, rev int64, at time.Time) config.Event {
		return config.Event{ID: id, At: at, Kind: config.EventGrantsChanged, StorageID: info.StorageID,
			Account: &config.AccountChange{Revision: rev, Grants: &grants}}
	}
	w.mail.set(func(f *fakeMail) { f.events = []config.Event{event(1, 1, w.now)} })
	if err := svc.recordMailEvents(ctx); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(time.Minute)
	if _, err := svc.RemoveMailboxGrant(ctx, bob, idString(w.bobBox), anna.ID); err != nil {
		t.Fatal(err)
	}
	if p := pushedMailbox(t, w.mail.last(), "info@bobsite.example.com"); p.GrantsSetAt != w.now.Unix() {
		t.Errorf("pushed grants time %d, want %d", p.GrantsSetAt, w.now.Unix())
	}
	// Made 30 s before the removal, reported after it.
	w.mail.set(func(f *fakeMail) { f.events = append(f.events, event(2, 2, w.now.Add(-30*time.Second))) })
	if err := svc.recordMailEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ := svc.MailboxOf(ctx, bob, idString(w.bobBox)); len(v.SharedWith) != 0 {
		t.Fatalf("late event undid the removal: %+v", v.SharedWith)
	}
	// Made after the removal: taken.
	w.now = w.now.Add(time.Second)
	w.mail.set(func(f *fakeMail) { f.events = append(f.events, event(3, 3, w.now)) })
	if err := svc.recordMailEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ := svc.MailboxOf(ctx, bob, idString(w.bobBox)); len(v.SharedWith) != 1 {
		t.Fatalf("newer event not taken: %+v", v.SharedWith)
	}
}
