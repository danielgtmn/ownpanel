package directory

import (
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

const h = "$argon2id$v=19$m=65536,t=2,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA"

func sid(c byte) string { return strings.Repeat(string(c), 32) }

func sharingDir() *Directory {
	return New(&config.Config{
		Hostname: "mail.example.com",
		Domains: []config.Domain{
			{Name: "example.com", Organisation: "org-acme"},
			{Name: "example.de", Organisation: "org-acme"},
			{Name: "other.example"},
		},
		Mailboxes: []config.Mailbox{
			{StorageID: sid('a'), Address: "anna@example.com", PasswordHash: h},
			{StorageID: sid('b'), Address: "ben@example.de", PasswordHash: h},
			{StorageID: sid('c'), Address: "carl@other.example", PasswordHash: h},
			{StorageID: sid('d'), Address: "team@example.com", Kind: config.KindShared, Grants: []config.Grant{
				{StorageID: sid('a'), Rights: []string{config.RightWrite, config.RightSendAs}},
				{StorageID: sid('b'), Rights: []string{config.RightRead, config.RightSendOnBehalf}},
				// Not counted: another organisation (as if regrouped after the push).
				{StorageID: sid('c'), Rights: []string{config.RightRead}},
			}},
		},
		Aliases: []config.Alias{
			{Address: "hello@example.com", Targets: []string{"team@example.com"}},
			{Address: "staff@example.com", Targets: []string{"anna@example.com", "ben@example.de"}, SenderPolicy: config.SenderMembers},
			{Address: "news@example.com", Targets: []string{"x@outside.example"}, SenderPolicy: config.SenderOrganisation},
			{Address: "anna.alt@example.com", Targets: []string{"anna@example.com"}},
		},
	})
}

func TestRightsAndAccess(t *testing.T) {
	d := sharingDir()
	if r := d.Rights(sid('d'), sid('a')); !r.Has(config.Read | config.Write | config.SendAs) {
		t.Errorf("anna %v", r)
	}
	if r := d.Rights(sid('d'), sid('c')); r != 0 {
		t.Errorf("carl across organisations: %v", r)
	}
	if r := d.Rights(sid('a'), sid('a')); r != config.AllRights {
		t.Errorf("own %v", r)
	}
	if r := d.Rights(sid('d'), sid('d')); r != 0 {
		t.Errorf("a shared mailbox on itself: %v", r)
	}
	if a := d.Access(sid('a')); len(a) != 1 || a[0].Mailbox.Address != "team@example.com" {
		t.Errorf("access %v", a)
	}
	if g := d.Grantees(sid('d')); len(g) != 2 || g[0].Mailbox.Address != "anna@example.com" {
		t.Errorf("grantees %v", g)
	}
	if ig := d.Ignored(); len(ig) != 1 || !strings.Contains(ig[0], "another organisation") {
		t.Errorf("ignored %v", ig)
	}
}

func TestSendMode(t *testing.T) {
	d := sharingDir()
	anna, _ := d.Mailbox("anna@example.com")
	ben, _ := d.Mailbox("ben@example.de")
	carl, _ := d.Mailbox("carl@other.example")
	for _, tc := range []struct {
		m    *config.Mailbox
		addr string
		want SendMode
	}{
		{anna, "anna@example.com", SendOwn},
		{anna, "anna.alt@example.com", SendOwn},
		{anna, "team@example.com", SendAsGrant},
		{anna, "hello@example.com", SendAsGrant}, // an alias of the shared mailbox
		{ben, "team@example.com", SendOnBehalfGrant},
		{ben, "hello@example.com", SendDenied}, // on behalf: the address itself only
		{carl, "team@example.com", SendDenied},
		{anna, "ben@example.de", SendDenied},
	} {
		got, owner := d.SendMode(tc.m, tc.addr)
		if got != tc.want {
			t.Errorf("%s as %s: %v, want %v", tc.m.Address, tc.addr, got, tc.want)
		}
		if got == SendAsGrant || got == SendOnBehalfGrant {
			if owner == nil || owner.Address != "team@example.com" {
				t.Errorf("%s as %s: owner %v", tc.m.Address, tc.addr, owner)
			}
		}
	}
}

func TestGroupAllows(t *testing.T) {
	d := sharingDir()
	staff, _ := d.Group("staff@example.com")
	news, _ := d.Group("news@example.com")
	hello, _ := d.Group("hello@example.com")
	team, _ := d.Mailbox("team@example.com")
	anna, _ := d.Mailbox("anna@example.com")
	for _, tc := range []struct {
		g      *config.Alias
		sender string
		boxes  []*config.Mailbox
		want   bool
	}{
		{hello, "", nil, true},
		{hello, "someone@outside.example", nil, true},
		{staff, "anna@example.com", nil, true},
		{staff, "anna.alt@example.com", nil, true}, // an alias of a member
		{staff, "ben@example.de", nil, true},
		{staff, "team@example.com", nil, false},
		{staff, "team@example.com", []*config.Mailbox{team, anna}, true}, // anna sends as the team
		{staff, "carl@other.example", nil, false},
		{staff, "", nil, false},
		{news, "ben@example.de", nil, true}, // same organisation, other domain
		{news, "carl@other.example", nil, false},
		{news, "someone@outside.example", nil, false},
	} {
		if got := d.GroupAllows(tc.g, tc.sender, tc.boxes...); got != tc.want {
			t.Errorf("%s from %q: %v, want %v", tc.g.Address, tc.sender, got, tc.want)
		}
	}
}

func TestOverlayGrants(t *testing.T) {
	cfg := &config.Config{
		Hostname: "mail.example.com",
		Domains:  []config.Domain{{Name: "example.com"}},
		Mailboxes: []config.Mailbox{
			{StorageID: sid('a'), Address: "anna@example.com", PasswordHash: h},
			{StorageID: sid('b'), Address: "ben@example.com", PasswordHash: h},
			{StorageID: sid('d'), Address: "team@example.com", Kind: config.KindShared},
		},
	}
	grants := []config.Grant{{StorageID: sid('b'), Rights: []string{config.RightRead}}}
	o := &Overlay{}
	o.Apply(config.AccountChange{Revision: 1, Grants: &grants}, time.Now())
	d := New(Merge(cfg, map[string]*Overlay{sid('a'): o, sid('d'): o}))
	if !d.Rights(sid('a'), sid('b')).Has(config.Read) {
		t.Error("self-delegation not applied")
	}
	if d.Rights(sid('d'), sid('b')) != 0 {
		t.Error("an overlay changed the grants of a shared mailbox")
	}
	// A shared mailbox never gets a password back from an old overlay.
	pw := &Overlay{}
	pw.Apply(config.AccountChange{Revision: 1, PasswordHash: h}, time.Now())
	d = New(Merge(cfg, map[string]*Overlay{sid('d'): pw}))
	if m, _ := d.Mailbox("team@example.com"); m.PasswordHash != "" {
		t.Error("shared mailbox got a password")
	}
	o.Prune(1)
	if !o.Empty() {
		t.Error("grants not pruned")
	}
}
