package config

import (
	"strings"
	"testing"
)

// TestDropInvalidKeepsSharedMailbox: a shared mailbox that got a password or
// app passwords loses those, not its place in the configuration.
func TestDropInvalidKeepsSharedMailbox(t *testing.T) {
	c := sharingConfig()
	c.AppPasswordKey = NewAppPasswordKey()
	id, _, _ := NewAppPassword()
	team := &c.Mailboxes[3]
	team.PasswordHash, team.AppPasswordsOnly = testHash, true
	team.AppPasswords = []AppPassword{{ID: id, Name: "Phone", MAC: strings.Repeat("ab", 32)}}
	notes := c.DropInvalid()
	if err := c.Validate(); err != nil {
		t.Fatalf("after DropInvalid: %v", err)
	}
	var kept *Mailbox
	for i := range c.Mailboxes {
		if c.Mailboxes[i].Address == "team@example.com" {
			kept = &c.Mailboxes[i]
		}
	}
	if kept == nil || kept.PasswordHash != "" || len(kept.AppPasswords) != 0 || kept.AppPasswordsOnly || len(kept.Grants) != 2 {
		t.Fatalf("team %+v\n%s", kept, strings.Join(notes, "\n"))
	}
}
