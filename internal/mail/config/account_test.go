package config

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestAppPasswordToken(t *testing.T) {
	id, secret, token := NewAppPassword()
	if len(id) != AppPasswordIDLen || len(secret) != AppPasswordSecretLen || token != "op_"+id+"_"+secret {
		t.Fatalf("token %q", token)
	}
	gid, gsecret, ok := ParseAppPassword(token)
	if !ok || gid != id || gsecret != secret || !LooksLikeAppPassword(token) || !ValidAppPasswordID(id) {
		t.Fatal("parse")
	}
	for _, bad := range []string{"", token + "a", strings.ToUpper(token), "op_" + id + secret, "op_" + id + "_" + secret[:31] + "1", " " + token} {
		if _, _, ok := ParseAppPassword(bad); ok {
			t.Errorf("ParseAppPassword(%q) ok", bad)
		}
	}
	_, _, other := NewAppPassword()
	if other == token {
		t.Fatal("tokens repeat")
	}

	key := NewAppPasswordKey()
	sid := strings.Repeat("a", 32)
	mac := AppPasswordMAC(key, sid, id, secret)
	if len(mac) != 64 || !macRe.MatchString(mac) {
		t.Fatalf("mac %q", mac)
	}
	ap := AppPassword{ID: id, MAC: mac}
	if !CheckAppPassword(key, sid, ap, secret) {
		t.Fatal("check")
	}
	// Bound to the key, the mailbox and the id.
	if CheckAppPassword(NewAppPasswordKey(), sid, ap, secret) || CheckAppPassword(key, strings.Repeat("b", 32), ap, secret) ||
		CheckAppPassword(key, sid, AppPassword{ID: "aaaaaaaaaaaaaaaa", MAC: mac}, secret) || CheckAppPassword(key, sid, ap, secret[:31]+"a") {
		t.Fatal("MAC not bound")
	}
	if AppPasswordMAC("zz", sid, id, secret) != "" || CheckAppPassword("", sid, ap, secret) {
		t.Fatal("malformed key accepted")
	}
}

func TestValidDisplayName(t *testing.T) {
	for _, s := range []string{"", "Anna Example", "Zoë Müller-Lüdenscheidt", "Support (EU)", "O'Brien", strings.Repeat("ä", 64)} {
		if !ValidDisplayName(s) {
			t.Errorf("ValidDisplayName(%q) = false", s)
		}
	}
	for _, s := range []string{"a\r\nBcc: x@example.org", "a\nb", "a\tb", "<x@example.org>", `"Anna"`, " Anna", "Anna ", strings.Repeat("a", 65),
		"a\u202eb", "a\u2028b", "a\x00b", "\xff"} {
		if ValidDisplayName(s) {
			t.Errorf("ValidDisplayName(%q) = true", s)
		}
	}
}

func TestNetwork(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.77": "192.0.2.0/24", "::ffff:192.0.2.77": "192.0.2.0/24", "2001:db8:aa:bb::1": "2001:db8:aa::/48",
	} {
		if got := Network(netip.MustParseAddr(in)); got != want {
			t.Errorf("Network(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestValidateAccounts(t *testing.T) {
	id, secret, _ := NewAppPassword()
	key := NewAppPasswordKey()
	sid := "0123456789abcdef0123456789abcdef"
	base := func() *Config {
		return &Config{
			Hostname:       "mail.example.com",
			AppPasswordKey: key,
			Domains:        []Domain{{Name: "example.com", Organisation: "org-0123456789abcdef"}},
			Mailboxes: []Mailbox{{
				StorageID: sid, Address: "a@example.com", DisplayName: "Anna",
				PasswordHash: "$argon2id$v=19$m=65536,t=2,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
				AppPasswords: []AppPassword{{ID: id, Name: "Phone", MAC: AppPasswordMAC(key, sid, id, secret), Created: time.Now()}},
				Revision:     3,
			}},
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(c *Config){
		"bad display name": func(c *Config) { c.Mailboxes[0].DisplayName = "a\nb" },
		"no key":           func(c *Config) { c.AppPasswordKey = "" },
		"short key":        func(c *Config) { c.AppPasswordKey = "abcd" },
		"upper key":        func(c *Config) { c.AppPasswordKey = strings.ToUpper(key) },
		"bad id":           func(c *Config) { c.Mailboxes[0].AppPasswords[0].ID = "ABC" },
		"bad mac":          func(c *Config) { c.Mailboxes[0].AppPasswords[0].MAC = "xyz" },
		"empty name":       func(c *Config) { c.Mailboxes[0].AppPasswords[0].Name = "" },
		"twice": func(c *Config) {
			c.Mailboxes[0].AppPasswords = append(c.Mailboxes[0].AppPasswords, c.Mailboxes[0].AppPasswords[0])
		},
		"bad organisation":  func(c *Config) { c.Domains[0].Organisation = "Org With Spaces" },
		"negative revision": func(c *Config) { c.Mailboxes[0].Revision = -1 },
		"too many": func(c *Config) {
			for range MaxAppPasswords {
				i, _, _ := NewAppPassword()
				c.Mailboxes[0].AppPasswords = append(c.Mailboxes[0].AppPasswords, AppPassword{ID: i, Name: "x", MAC: strings.Repeat("0", 64)})
			}
		},
	} {
		c := base()
		mod(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The key is part of the fingerprint: a new key is pushed.
	a, b := base(), base()
	b.AppPasswordKey = NewAppPasswordKey()
	if a.Hash() == b.Hash() {
		t.Error("hash ignores the app password key")
	}
}
