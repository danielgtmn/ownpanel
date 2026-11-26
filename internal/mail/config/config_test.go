package config

import (
	"strings"
	"testing"
)

func TestValidLocalPart(t *testing.T) {
	for _, l := range []string{"info", "first.last", "a+tag", "x_y-z"} {
		if !ValidLocalPart(l) {
			t.Errorf("ValidLocalPart(%q) = false, want true", l)
		}
	}
	// "*" means catch-all only in aliases; a mailbox may never be called that.
	for _, l := range []string{"*", "", ".a", "a.", "a..b", "Info", "a b", "ü"} {
		if ValidLocalPart(l) {
			t.Errorf("ValidLocalPart(%q) = true, want false", l)
		}
	}
}

func TestValidateRefusesCatchAllMailbox(t *testing.T) {
	c := &Config{
		Hostname: "mail.example.com",
		Domains:  []Domain{{Name: "example.com"}},
		Mailboxes: []Mailbox{{
			StorageID:    "0123456789abcdef0123456789abcdef",
			Address:      "*@example.com",
			PasswordHash: "$argon2id$v=19$m=65536,t=2,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		}},
	}
	if err := c.Validate(); err == nil {
		t.Fatal("a mailbox named * was accepted")
	}
}

func TestHashIgnoresOrder(t *testing.T) {
	a := &Config{Hostname: "mail.example.com", Domains: []Domain{{Name: "a.example"}, {Name: "b.example"}}}
	b := &Config{Hostname: "mail.example.com", Domains: []Domain{{Name: "b.example"}, {Name: "a.example"}}}
	if a.Hash() != b.Hash() {
		t.Fatal("hash depends on the order of domains")
	}
}

// TestDropInvalid: what one domain, mailbox or alias gets wrong is left
// out, and the rest passes Validate.
func TestDropInvalid(t *testing.T) {
	hash := "$argon2id$v=19$m=65536,t=2,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA"
	mac := strings.Repeat("ab", 32)
	aps := []AppPassword{}
	for i := 0; i < MaxAppPasswords+2; i++ {
		id, _, _ := NewAppPassword()
		aps = append(aps, AppPassword{ID: id, Name: "Device", MAC: mac})
	}
	c := &Config{
		Hostname:       "mail.example.com",
		AppPasswordKey: NewAppPasswordKey(),
		Domains: []Domain{
			{Name: "example.com", Organisation: "dom-" + strings.Repeat("x", 200)},
			{Name: "Bad..domain"},
			{Name: "nodkim.example", DKIM: []DKIMKey{{Selector: "-bad", Active: true}}},
		},
		Mailboxes: []Mailbox{
			{StorageID: strings.Repeat("a", 32), Address: "ok@example.com", PasswordHash: hash, AppPasswords: aps},
			{StorageID: strings.Repeat("b", 32), Address: "broken@example.com", PasswordHash: "x"},
			{StorageID: strings.Repeat("c", 32), Address: "gone@bad..domain", PasswordHash: hash},
			{StorageID: strings.Repeat("d", 32), Address: "name@example.com", PasswordHash: hash, DisplayName: "a\nb",
				AppPasswords: []AppPassword{aps[0]}},
		},
		Aliases: []Alias{
			{Address: "sales@example.com", Targets: []string{"ok@example.com", "not an address"}},
			{Address: "ok@example.com", Targets: []string{"x@example.org"}},
			{Address: "empty@example.com", Targets: []string{"bad"}},
		},
	}
	if c.Validate() == nil {
		t.Fatal("test configuration is valid")
	}
	notes := c.DropInvalid()
	if err := c.Validate(); err != nil {
		t.Fatalf("still not valid: %v (%v)", err, notes)
	}
	if len(c.Domains) != 2 || c.Domains[0].Organisation != "" || len(c.Domains[1].DKIM) != 0 {
		t.Fatalf("domains %+v", c.Domains)
	}
	if len(c.Mailboxes) != 2 || len(c.Mailboxes[0].AppPasswords) != MaxAppPasswords ||
		c.Mailboxes[1].DisplayName != "" || len(c.Mailboxes[1].AppPasswords) != 0 {
		t.Fatalf("mailboxes %d", len(c.Mailboxes))
	}
	if len(c.Aliases) != 1 || len(c.Aliases[0].Targets) != 1 {
		t.Fatalf("aliases %+v", c.Aliases)
	}
	for _, n := range notes {
		if strings.Contains(n, mac) || strings.Contains(n, hash) || strings.Contains(n, c.AppPasswordKey) {
			t.Fatalf("secret in %q", n)
		}
	}
	// A valid configuration is left as it is.
	if again := c.DropInvalid(); len(again) != 0 {
		t.Fatalf("valid configuration changed: %v", again)
	}
}

func TestValidImportID(t *testing.T) {
	for _, id := range []string{"1", "42", "01jx3k5m7n9p1q3r5s7t9v1w3x", strings.Repeat("a", 32)} {
		if !ValidImportID(id) {
			t.Errorf("%q should be valid", id)
		}
	}
	for _, id := range []string{"", "..", "../x", "a/b", "A1", "1.spool", "a b", "-1", strings.Repeat("a", 33), "ä"} {
		if ValidImportID(id) {
			t.Errorf("%q should be invalid", id)
		}
	}
}
