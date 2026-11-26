package submission

import (
	"context"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// TestSendForBlockedOrLimited: every mailbox a message is sent for counts,
// not only the envelope sender's and the first From address's.
func TestSendForBlockedOrLimited(t *testing.T) {
	e := start(t)
	e.withSharing(func(c *config.Config) {
		for i := range c.Mailboxes {
			if c.Mailboxes[i].StorageID == idTeam {
				c.Mailboxes[i].SendPerHour = 1
			}
		}
	})
	a := e.client(alice)
	// The shared mailbox reaches its limit.
	if err := send(a, team, []string{bob}, message(team, bob, "one")); err != nil {
		t.Fatal(err)
	}
	// Named second in From, with alice's own envelope and Sender.
	body := withSender(message(alice+", "+team, bob, "second in From"), alice)
	if err := send(a, alice, []string{bob}, body); code(err) != 451 {
		t.Errorf("shared mailbox second in From over its limit: %v", err)
	}
	// As the Sender (held through send-as).
	body = withSender(message(alice, bob, "team as Sender"), team)
	if err := send(a, alice, []string{bob}, body); code(err) != 451 {
		t.Errorf("shared mailbox as Sender over its limit: %v", err)
	}

	// A blocked shared mailbox cannot be named in From with an own envelope.
	e2 := start(t)
	e2.withSharing(nil)
	if err := e2.store.SetBlocked(context.Background(), idTeam, "test"); err != nil {
		t.Fatal(err)
	}
	a2 := e2.client(alice)
	if err := send(a2, alice, []string{bob}, message(team, bob, "blocked")); code(err) != 550 {
		t.Errorf("blocked shared mailbox in From: %v", err)
	}
	if err := send(a2, alice, []string{bob}, withSender(message(alice, bob, "blocked sender"), team)); code(err) != 550 {
		t.Errorf("blocked shared mailbox as Sender: %v", err)
	}
	if err := send(a2, alice, []string{bob}, message(alice, bob, "own")); err != nil {
		t.Errorf("own mail: %v", err)
	}
}
