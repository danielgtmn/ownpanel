package config

import "testing"

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
