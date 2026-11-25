package smtpd

import (
	"testing"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// TestGroupSenderPolicy: a distribution group limited to its members or its
// organisation refuses others at RCPT; the sender must then also pass an
// aligned SPF or DKIM check like all mail of our domains.
func TestGroupSenderPolicy(t *testing.T) {
	cfg := testConfig(t)
	cfg.Aliases = append(cfg.Aliases,
		config.Alias{Address: "staff@example.org", Targets: []string{"alice@example.org"}, SenderPolicy: config.SenderMembers},
		config.Alias{Address: "org@example.org", Targets: []string{"bob@example.org"}, SenderPolicy: config.SenderOrganisation},
	)
	e := start(t, cfg)
	e.res.TXT["example.org"] = []string{"v=spf1 ip4:192.0.2.0/24 -all"}

	// From outside: refused, whatever the policy.
	e.from("203.0.113.5")
	c := dial(t, e.addr, false)
	for _, rcpt := range []string{"staff@example.org", "org@example.org"} {
		if err := c.Mail("someone@sender.test", nil); err != nil {
			t.Fatal(err)
		}
		if err := c.Rcpt(rcpt, nil); code(err) != 550 {
			t.Errorf("outside sender to %s: %v", rcpt, err)
		}
		c.Reset()
	}
	// The null sender is nobody's member.
	if err := c.Mail("", nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Rcpt("staff@example.org", nil); code(err) != 550 {
		t.Errorf("null sender: %v", err)
	}
	c.Reset()

	// A member with an SPF pass of the domain.
	e.from("192.0.2.20")
	c = dial(t, e.addr, false)
	msg := "From: alice@example.org\r\nSubject: hi\r\n\r\nHello.\r\n"
	if err := send(c, "alice@example.org", []string{"staff@example.org"}, msg); err != nil {
		t.Errorf("member: %v", err)
	}
	// The organisation, not a member.
	if err := c.Mail("bob@example.org", nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Rcpt("staff@example.org", nil); code(err) != 550 {
		t.Errorf("non-member: %v", err)
	}
	c.Reset()
	if err := send(c, "bob@example.org", []string{"org@example.org"}, "From: bob@example.org\r\nSubject: hi\r\n\r\nHello.\r\n"); err != nil {
		t.Errorf("organisation: %v", err)
	}
	if n := len(inbox(t, e, idAlice)); n != 1 {
		t.Errorf("alice has %d messages", n)
	}

	// Claiming a member's address without SPF or DKIM passes RCPT but not
	// the check of our own domains.
	e.from("198.51.100.9")
	c = dial(t, e.addr, false)
	if err := send(c, "alice@example.org", []string{"staff@example.org"}, msg); code(err) != 550 {
		t.Errorf("forged member: %v", err)
	}
}
