package config

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

const testHash = "$argon2id$v=19$m=65536,t=2,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA"

func sid(c byte) string { return strings.Repeat(string(c), 32) }

// sharingConfig: example.com and example.de form one organisation,
// other.example stands alone. team@ is shared.
func sharingConfig() *Config {
	return &Config{
		Hostname: "mail.example.com",
		Domains: []Domain{
			{Name: "example.com", Organisation: "org-acme"},
			{Name: "example.de", Organisation: "org-acme"},
			{Name: "other.example"},
		},
		Mailboxes: []Mailbox{
			{StorageID: sid('a'), Address: "anna@example.com", PasswordHash: testHash},
			{StorageID: sid('b'), Address: "ben@example.de", PasswordHash: testHash},
			{StorageID: sid('c'), Address: "carl@other.example", PasswordHash: testHash},
			{StorageID: sid('d'), Address: "team@example.com", Kind: KindShared, KeepSentCopy: true, Grants: []Grant{
				{StorageID: sid('a'), Rights: []string{RightWrite, RightSendAs}},
				{StorageID: sid('b'), Rights: []string{RightRead, RightSendOnBehalf}},
			}},
		},
		Aliases: []Alias{{Address: "all@example.com", Targets: []string{"anna@example.com", "ben@example.de"}, SenderPolicy: SenderMembers, HideFromDirectory: true}},
	}
}

func TestParseRights(t *testing.T) {
	r, err := ParseRights([]string{RightWrite})
	if err != nil || !r.Has(Read|Write) || r.Has(SendAs) {
		t.Fatalf("write: %v %v", r, err)
	}
	if got := r.Names(); !slices.Equal(got, []string{RightRead, RightWrite}) {
		t.Errorf("names %v", got)
	}
	for _, bad := range [][]string{nil, {}, {"admin"}, {RightRead, RightRead}} {
		if _, err := ParseRights(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	all, _ := ParseRights([]string{RightRead, RightWrite, RightManage, RightSendAs, RightSendOnBehalf})
	if all != AllRights {
		t.Errorf("all = %v", all)
	}
}

func TestValidateSharing(t *testing.T) {
	if err := sharingConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(c *Config){
		"shared with a password": func(c *Config) { c.Mailboxes[3].PasswordHash = testHash },
		"shared with app passwords only": func(c *Config) {
			c.Mailboxes[3].AppPasswordsOnly = true
		},
		"personal without password":  func(c *Config) { c.Mailboxes[0].PasswordHash = "" },
		"personal keeps sent copies": func(c *Config) { c.Mailboxes[0].KeepSentCopy = true },
		"unknown kind":               func(c *Config) { c.Mailboxes[0].Kind = "resource" },
		"grant to itself": func(c *Config) {
			c.Mailboxes[3].Grants = append(c.Mailboxes[3].Grants, Grant{StorageID: sid('d'), Rights: []string{RightRead}})
		},
		"grant twice": func(c *Config) {
			c.Mailboxes[3].Grants = append(c.Mailboxes[3].Grants, Grant{StorageID: sid('a'), Rights: []string{RightRead}})
		},
		"grant without rights": func(c *Config) { c.Mailboxes[3].Grants[0].Rights = nil },
		"grant to another organisation": func(c *Config) {
			c.Mailboxes[3].Grants = append(c.Mailboxes[3].Grants, Grant{StorageID: sid('c'), Rights: []string{RightRead}})
		},
		"grant to a missing mailbox": func(c *Config) {
			c.Mailboxes[3].Grants = append(c.Mailboxes[3].Grants, Grant{StorageID: sid('f'), Rights: []string{RightRead}})
		},
		"grant held by a shared mailbox": func(c *Config) {
			c.Mailboxes[0].Grants = []Grant{{StorageID: sid('d'), Rights: []string{RightRead}}}
		},
		"policy on the catch-all": func(c *Config) {
			c.Aliases = append(c.Aliases, Alias{Address: "*@example.com", Targets: []string{"anna@example.com"}, SenderPolicy: SenderMembers})
		},
		"unknown policy": func(c *Config) { c.Aliases[0].SenderPolicy = "friends" },
	} {
		c := sharingConfig()
		change(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A personal mailbox delegating itself within its organisation is fine.
	c := sharingConfig()
	c.Mailboxes[0].Grants = []Grant{{StorageID: sid('b'), Rights: []string{RightRead}}}
	if err := c.Validate(); err != nil {
		t.Errorf("self-delegation: %v", err)
	}
}

// TestDropInvalidSharing: bad grants are left out per mailbox; the mailbox
// and everything else stays.
func TestDropInvalidSharing(t *testing.T) {
	c := sharingConfig()
	team := &c.Mailboxes[3]
	team.Grants = append(team.Grants,
		Grant{StorageID: sid('c'), Rights: []string{RightRead}},    // other organisation
		Grant{StorageID: sid('d'), Rights: []string{RightRead}},    // itself
		Grant{StorageID: sid('a'), Rights: []string{RightRead}},    // twice
		Grant{StorageID: "nope", Rights: []string{RightRead}},      // not a storage id
		Grant{StorageID: sid('f'), Rights: []string{RightRead}},    // missing
		Grant{StorageID: sid('b'), Rights: []string{"everything"}}, // bad right (and twice)
	)
	c.Mailboxes[0].KeepSentCopy = true
	c.Mailboxes[0].Grants = []Grant{{StorageID: sid('d'), Rights: []string{RightRead}}} // to a shared mailbox
	c.Mailboxes[1].PasswordHash = "broken"                                              // left out: grants to it go too
	c.Aliases[0].SenderPolicy = "friends"
	notes := c.DropInvalid()
	if err := c.Validate(); err != nil {
		t.Fatalf("after DropInvalid: %v\n%s", err, strings.Join(notes, "\n"))
	}
	if len(c.Mailboxes) != 3 {
		t.Fatalf("mailboxes %v", c.Mailboxes)
	}
	var shared *Mailbox
	for i := range c.Mailboxes {
		if c.Mailboxes[i].Shared() {
			shared = &c.Mailboxes[i]
		}
	}
	if shared == nil || len(shared.Grants) != 1 || shared.Grants[0].StorageID != sid('a') || !shared.KeepSentCopy {
		t.Errorf("team %+v", shared)
	}
	if c.Mailboxes[0].KeepSentCopy || len(c.Mailboxes[0].Grants) != 0 {
		t.Errorf("anna %+v", c.Mailboxes[0])
	}
	if c.Aliases[0].SenderPolicy != SenderMembers {
		t.Errorf("policy %q", c.Aliases[0].SenderPolicy)
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{"another organisation", "listed twice", "does not exist", "shared mailbox", "sender policy"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes miss %q:\n%s", want, joined)
		}
	}
}

// TestHashUnchangedWithoutSharing: configurations without shared
// mailboxes hash as before (the new fields are left out).
func TestHashUnchangedWithoutSharing(t *testing.T) {
	c := &Config{Hostname: "mail.example.com", Domains: []Domain{{Name: "example.com"}},
		Mailboxes: []Mailbox{{StorageID: sid('a'), Address: "anna@example.com", PasswordHash: testHash}},
		Aliases:   []Alias{{Address: "x@example.com", Targets: []string{"anna@example.com"}}}}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"kind", "grants", "keep_sent_copy", "sender_policy", "hide_from_directory"} {
		if strings.Contains(string(b), field) {
			t.Errorf("%s in %s", field, b)
		}
	}
}
